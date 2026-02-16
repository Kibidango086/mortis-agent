// mortisagent - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 mortisagent contributors

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Kibidango086/mortis-agent/pkg/bus"
	"github.com/Kibidango086/mortis-agent/pkg/config"
	"github.com/Kibidango086/mortis-agent/pkg/constants"
	"github.com/Kibidango086/mortis-agent/pkg/logger"
	"github.com/Kibidango086/mortis-agent/pkg/providers"
	"github.com/Kibidango086/mortis-agent/pkg/session"
	"github.com/Kibidango086/mortis-agent/pkg/state"
	"github.com/Kibidango086/mortis-agent/pkg/tools"
	"github.com/Kibidango086/mortis-agent/pkg/utils"
)

type AgentLoop struct {
	bus            *bus.MessageBus
	provider       providers.LLMProvider
	workspace      string
	model          string
	contextWindow  int // Maximum context window size in tokens
	maxIterations  int
	sessions       *session.SessionManager
	state          *state.Manager
	contextBuilder *ContextBuilder
	tools          *tools.ToolRegistry
	running        atomic.Bool
	summarizing    sync.Map // Tracks which sessions are currently being summarized
}

// processOptions configures how a message is processed
type processOptions struct {
	SessionKey      string   // Session identifier for history/context
	Channel         string   // Target channel for tool execution
	ChatID          string   // Target chat ID for tool execution
	UserMessage     string   // User message content (may include prefix)
	Media           []string // Media file paths (images, audio, etc.)
	DefaultResponse string   // Response when LLM returns empty
	EnableSummary   bool     // Whether to trigger summarization
	SendResponse    bool     // Whether to send response via bus
	NoHistory       bool     // If true, don't load session history (for heartbeat)
	StreamMode      bool     // 是否启用流式模式
}

// createToolRegistry creates a tool registry with common tools.
// This is shared between main agent and subagents.
func createToolRegistry(workspace string, restrict bool, cfg *config.Config, msgBus *bus.MessageBus) *tools.ToolRegistry {
	registry := tools.NewToolRegistry()

	// File system tools
	registry.Register(tools.NewReadFileTool(workspace, restrict))
	registry.Register(tools.NewWriteFileTool(workspace, restrict))
	registry.Register(tools.NewListDirTool(workspace, restrict))
	registry.Register(tools.NewEditFileTool(workspace, restrict))
	registry.Register(tools.NewAppendFileTool(workspace, restrict))

	// Shell execution
	registry.Register(tools.NewExecTool(workspace, restrict))

	if searchTool := tools.NewWebSearchTool(tools.WebSearchToolOptions{
		BraveAPIKey:          cfg.Tools.Web.Brave.APIKey,
		BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
		BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
		DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
		DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
	}); searchTool != nil {
		registry.Register(searchTool)
	}
	registry.Register(tools.NewWebFetchTool(50000))

	// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
	registry.Register(tools.NewI2CTool())
	registry.Register(tools.NewSPITool())

	// Search tools - enhanced file search
	registry.Register(tools.NewGlobTool(workspace, restrict))
	registry.Register(tools.NewGrepTool(workspace, restrict))

	// Todo management tool
	registry.Register(tools.NewTodoTool())

	// Question tool - allow AI to ask user
	questionTool := tools.NewQuestionTool(msgBus)
	registry.Register(questionTool)

	// Message tool - available to both agent and subagent
	// Subagent uses it to communicate directly with user
	messageTool := tools.NewMessageTool()
	messageTool.SetSendCallback(func(channel, chatID, content string) error {
		msgBus.PublishOutbound(bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: content,
		})
		return nil
	})
	registry.Register(messageTool)

	return registry
}

func NewAgentLoop(cfg *config.Config, msgBus *bus.MessageBus, provider providers.LLMProvider) *AgentLoop {
	workspace := cfg.WorkspacePath()
	os.MkdirAll(workspace, 0755)

	restrict := cfg.Agents.Defaults.RestrictToWorkspace

	// Create tool registry for main agent
	toolsRegistry := createToolRegistry(workspace, restrict, cfg, msgBus)

	// Create subagent manager with its own tool registry
	subagentManager := tools.NewSubagentManager(provider, cfg.Agents.Defaults.Model, workspace, msgBus)
	subagentTools := createToolRegistry(workspace, restrict, cfg, msgBus)
	// Subagent doesn't need spawn/subagent tools to avoid recursion
	subagentManager.SetTools(subagentTools)

	// Register spawn tool (for main agent)
	spawnTool := tools.NewSpawnTool(subagentManager)
	toolsRegistry.Register(spawnTool)

	// Register subagent tool (synchronous execution)
	subagentTool := tools.NewSubagentTool(subagentManager)
	toolsRegistry.Register(subagentTool)

	sessionsManager := session.NewSessionManager(filepath.Join(workspace, "sessions"))

	// Create state manager for atomic state persistence
	stateManager := state.NewManager(workspace)

	// Create context builder and set tools registry
	contextBuilder := NewContextBuilder(workspace)
	contextBuilder.SetToolsRegistry(toolsRegistry)

	return &AgentLoop{
		bus:            msgBus,
		provider:       provider,
		workspace:      workspace,
		model:          cfg.Agents.Defaults.Model,
		contextWindow:  cfg.Agents.Defaults.MaxTokens, // Restore context window for summarization
		maxIterations:  cfg.Agents.Defaults.MaxToolIterations,
		sessions:       sessionsManager,
		state:          stateManager,
		contextBuilder: contextBuilder,
		tools:          toolsRegistry,
		summarizing:    sync.Map{},
	}
}

func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)

	for al.running.Load() {
		select {
		case <-ctx.Done():
			return nil
		default:
			msg, ok := al.bus.ConsumeInbound(ctx)
			if !ok {
				continue
			}

			response, err := al.processMessage(ctx, msg)
			if err != nil {
				response = fmt.Sprintf("Error processing message: %v", err)
			}

			if response != "" {
				if msg.StreamMode {
					continue
				}

				alreadySent := false
				if tool, ok := al.tools.Get("message"); ok {
					if mt, ok := tool.(*tools.MessageTool); ok {
						alreadySent = mt.HasSentInRound()
					}
				}

				if !alreadySent {
					al.bus.PublishOutbound(bus.OutboundMessage{
						Channel: msg.Channel,
						ChatID:  msg.ChatID,
						Content: response,
					})
				}
			}
		}
	}

	return nil
}

func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	al.tools.Register(tool)
}

// RecordLastChannel records the last active channel for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChannel(channel string) error {
	return al.state.SetLastChannel(channel)
}

// RecordLastChatID records the last active chat ID for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChatID(chatID string) error {
	return al.state.SetLastChatID(chatID)
}

func (al *AgentLoop) ProcessDirect(ctx context.Context, content, sessionKey string) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct")
}

func (al *AgentLoop) ProcessDirectWithChannel(ctx context.Context, content, sessionKey, channel, chatID string) (string, error) {
	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   "cron",
		ChatID:     chatID,
		Content:    content,
		SessionKey: sessionKey,
	}

	return al.processMessage(ctx, msg)
}

// ProcessHeartbeat processes a heartbeat request without session history.
// Each heartbeat is independent and doesn't accumulate context.
func (al *AgentLoop) ProcessHeartbeat(ctx context.Context, content, channel, chatID string) (string, error) {
	return al.runAgentLoop(ctx, processOptions{
		SessionKey:      "heartbeat",
		Channel:         channel,
		ChatID:          chatID,
		UserMessage:     content,
		DefaultResponse: "I've completed processing but have no response to give.",
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       true, // Don't load session history for heartbeat
		StreamMode:      false,
	})
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	// Add message preview to log (show full content for error messages)
	var logContent string
	if strings.Contains(msg.Content, "Error:") || strings.Contains(msg.Content, "error") {
		logContent = msg.Content // Full content for errors
	} else {
		logContent = utils.Truncate(msg.Content, 80)
	}
	logger.InfoCF("agent", fmt.Sprintf("Processing message from %s:%s: %s", msg.Channel, msg.SenderID, logContent),
		map[string]interface{}{
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"sender_id":   msg.SenderID,
			"session_key": msg.SessionKey,
			"media_count": len(msg.Media),
			"stream_mode": msg.StreamMode,
		})

	// Route system messages to processSystemMessage
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg)
	}

	// Process as user message
	return al.runAgentLoop(ctx, processOptions{
		SessionKey:      msg.SessionKey,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		UserMessage:     msg.Content,
		Media:           msg.Media,
		DefaultResponse: "I've completed processing but have no response to give.",
		EnableSummary:   true,
		SendResponse:    false,
		StreamMode:      msg.StreamMode,
	})
}

func (al *AgentLoop) processSystemMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	// Verify this is a system message
	if msg.Channel != "system" {
		return "", fmt.Errorf("processSystemMessage called with non-system message channel: %s", msg.Channel)
	}

	logger.InfoCF("agent", "Processing system message",
		map[string]interface{}{
			"sender_id": msg.SenderID,
			"chat_id":   msg.ChatID,
		})

	// Parse origin channel from chat_id (format: "channel:chat_id")
	var originChannel string
	if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
		originChannel = msg.ChatID[:idx]
	} else {
		// Fallback
		originChannel = "cli"
	}

	// Extract subagent result from message content
	// Format: "Task 'label' completed.\n\nResult:\n<actual content>"
	content := msg.Content
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		content = content[idx+8:] // Extract just the result part
	}

	// Skip internal channels - only log, don't send to user
	if constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]interface{}{
				"sender_id":   msg.SenderID,
				"content_len": len(content),
				"channel":     originChannel,
			})
		return "", nil
	}

	// Agent acts as dispatcher only - subagent handles user interaction via message tool
	// Don't forward result here, subagent should use message tool to communicate with user
	logger.InfoCF("agent", "Subagent completed",
		map[string]interface{}{
			"sender_id":   msg.SenderID,
			"channel":     originChannel,
			"content_len": len(content),
		})

	// Agent only logs, does not respond to user
	return "", nil
}

// sendStreamEvent 发送流式事件到总线 (opencode 1:1 模式)
func (al *AgentLoop) sendStreamEvent(opts processOptions, eventType bus.StreamEventType, content string, toolName string, args map[string]interface{}, toolResult string, iteration int) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       eventType,
		Content:    content,
		ToolName:   toolName,
		ToolArgs:   args,
		ToolResult: toolResult,
		Iteration:  iteration,
		Timestamp:  time.Now(),
	})
}

// sendStreamEventWithID 发送带 ID 的流式事件 (opencode 模式)
func (al *AgentLoop) sendStreamEventWithID(opts processOptions, eventType bus.StreamEventType, id string, delta string, partID string) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       eventType,
		ID:         id,
		Delta:      delta,
		PartID:     partID,
		Timestamp:  time.Now(),
	})
}

// sendTextStart 发送 text-start 事件 (1:1 from opencode)
func (al *AgentLoop) sendTextStart(opts processOptions, partID string) {
	al.sendStreamEventWithID(opts, bus.StreamEventTextStart, "", "", partID)
}

// sendTextDelta 发送 text-delta 事件 (1:1 from opencode)
func (al *AgentLoop) sendTextDelta(opts processOptions, partID string, delta string) {
	al.sendStreamEventWithID(opts, bus.StreamEventTextDelta, "", delta, partID)
}

// sendTextEnd 发送 text-end 事件 (1:1 from opencode)
func (al *AgentLoop) sendTextEnd(opts processOptions, partID string) {
	al.sendStreamEventWithID(opts, bus.StreamEventTextEnd, "", "", partID)
}

// sendReasoningStart 发送 reasoning-start 事件 (1:1 from opencode)
func (al *AgentLoop) sendReasoningStart(opts processOptions, id string, partID string) {
	al.sendStreamEventWithID(opts, bus.StreamEventReasoningStart, id, "", partID)
}

// sendReasoningDelta 发送 reasoning-delta 事件 (1:1 from opencode)
func (al *AgentLoop) sendReasoningDelta(opts processOptions, id string, delta string) {
	al.sendStreamEventWithID(opts, bus.StreamEventReasoningDelta, id, delta, "")
}

// sendReasoningEnd 发送 reasoning-end 事件 (1:1 from opencode)
func (al *AgentLoop) sendReasoningEnd(opts processOptions, id string) {
	al.sendStreamEventWithID(opts, bus.StreamEventReasoningEnd, id, "", "")
}

// sendToolInputStart 发送 tool-input-start 事件 (1:1 from opencode)
func (al *AgentLoop) sendToolInputStart(opts processOptions, toolCallID string, toolName string, partID string) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       bus.StreamEventToolInputStart,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		PartID:     partID,
		Timestamp:  time.Now(),
	})
}

// sendToolCall 发送 tool-call 事件 (1:1 from opencode)
func (al *AgentLoop) sendToolCall(opts processOptions, toolCallID string, toolName string, args map[string]interface{}, iteration int) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       bus.StreamEventToolCall,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		ToolArgs:   args,
		Iteration:  iteration,
		Timestamp:  time.Now(),
	})
}

// sendToolResult 发送 tool-result 事件 (1:1 from opencode)
func (al *AgentLoop) sendToolResult(opts processOptions, toolCallID string, toolName string, result string, iteration int) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       bus.StreamEventToolResult,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		ToolResult: result,
		Iteration:  iteration,
		Timestamp:  time.Now(),
	})
}

// sendToolError 发送 tool-error 事件 (1:1 from opencode)
func (al *AgentLoop) sendToolError(opts processOptions, toolCallID string, toolName string, err string, iteration int) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       bus.StreamEventToolError,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Error:      err,
		Iteration:  iteration,
		Timestamp:  time.Now(),
	})
}

// sendStartStep 发送 start-step 事件 (1:1 from opencode)
func (al *AgentLoop) sendStartStep(opts processOptions, iteration int) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       bus.StreamEventStartStep,
		Iteration:  iteration,
		Timestamp:  time.Now(),
	})
}

// sendFinishStep 发送 finish-step 事件 (1:1 from opencode)
func (al *AgentLoop) sendFinishStep(opts processOptions, iteration int, finishReason string) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:      opts.Channel,
		ChatID:       opts.ChatID,
		SessionKey:   opts.SessionKey,
		Type:         bus.StreamEventFinishStep,
		Iteration:    iteration,
		FinishReason: finishReason,
		Timestamp:    time.Now(),
	})
}

// sendStart 发送 start 事件 (1:1 from opencode)
func (al *AgentLoop) sendStart(opts processOptions) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       bus.StreamEventStart,
		Timestamp:  time.Now(),
	})
}

// sendFinish 发送 finish 事件 (1:1 from opencode)
func (al *AgentLoop) sendFinish(opts processOptions) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       bus.StreamEventFinish,
		Timestamp:  time.Now(),
	})
}

// sendError 发送 error 事件 (1:1 from opencode)
func (al *AgentLoop) sendError(opts processOptions, err string) {
	if !opts.StreamMode {
		return
	}

	al.bus.PublishStream(bus.StreamMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		SessionKey: opts.SessionKey,
		Type:       bus.StreamEventError,
		Content:    err,
		Timestamp:  time.Now(),
	})
}

// runAgentLoop is the core message processing logic.
// It handles context building, LLM calls, tool execution, and response handling.
func (al *AgentLoop) runAgentLoop(ctx context.Context, opts processOptions) (string, error) {
	// 0. Record last channel for heartbeat notifications (skip internal channels)
	if opts.Channel != "" && opts.ChatID != "" {
		// Don't record internal channels (cli, system, subagent)
		if !constants.IsInternalChannel(opts.Channel) {
			channelKey := fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID)
			if err := al.RecordLastChannel(channelKey); err != nil {
				logger.WarnCF("agent", "Failed to record last channel: %v", map[string]interface{}{"error": err.Error()})
			}
		}
	}

	// 发送 start 事件 (1:1 from opencode)
	al.sendStart(opts)

	// 发送 start-step 事件 (1:1 from opencode)
	al.sendStartStep(opts, 1)

	// 1. Update tool contexts
	al.updateToolContexts(opts.Channel, opts.ChatID)

	// 2. Build messages (skip history for heartbeat)
	var history []providers.Message
	var summary string
	if !opts.NoHistory {
		history = al.sessions.GetHistory(opts.SessionKey)
		summary = al.sessions.GetSummary(opts.SessionKey)
	}
	messages := al.contextBuilder.BuildMessages(
		history,
		summary,
		opts.UserMessage,
		opts.Media,
		opts.Channel,
		opts.ChatID,
	)

	// 3. Save user message to session
	al.sessions.AddMessage(opts.SessionKey, "user", opts.UserMessage)

	// 4. Run LLM iteration loop
	finalContent, iteration, err := al.runLLMIteration(ctx, messages, opts)
	if err != nil {
		al.sendStreamEvent(opts, bus.StreamEventError, fmt.Sprintf("错误: %v", err), "", nil, "", iteration)
		return "", err
	}

	// If last tool had ForUser content and we already sent it, we might not need to send final response
	// This is controlled by the tool's Silent flag and ForUser content

	// 5. Handle empty response
	if finalContent == "" {
		finalContent = opts.DefaultResponse
	}

	// 6. Save final assistant message to session
	al.sessions.AddMessage(opts.SessionKey, "assistant", finalContent)
	al.sessions.Save(opts.SessionKey)

	// Send finish event (1:1 from opencode)
	al.sendFinish(opts)

	// 7. Optional: summarization
	if opts.EnableSummary {
		al.maybeSummarize(opts.SessionKey)
	}

	// 8. Optional: send response via bus
	if opts.SendResponse {
		al.bus.PublishOutbound(bus.OutboundMessage{
			Channel: opts.Channel,
			ChatID:  opts.ChatID,
			Content: finalContent,
		})
	}

	// 9. Log response
	responsePreview := utils.Truncate(finalContent, 120)
	logger.InfoCF("agent", fmt.Sprintf("Response: %s", responsePreview),
		map[string]interface{}{
			"session_key":  opts.SessionKey,
			"iterations":   iteration,
			"final_length": len(finalContent),
		})

	return finalContent, nil
}

// runLLMIteration executes the LLM call loop with tool handling.
// Returns the final content, iteration count, and any error.
// 1:1 from opencode's processor.ts stream handling
func (al *AgentLoop) runLLMIteration(ctx context.Context, messages []providers.Message, opts processOptions) (string, int, error) {
	iteration := 0
	var finalContent string

	// Create ID generator for this stream session
	idGen := NewStreamIDGenerator("part")

	for iteration < al.maxIterations {
		iteration++

		// Send start-step event (1:1 from opencode)
		al.sendStartStep(opts, iteration)

		logger.DebugCF("agent", "LLM iteration",
			map[string]interface{}{
				"iteration": iteration,
				"max":       al.maxIterations,
			})

		// Build tool definitions
		providerToolDefs := al.tools.ToProviderDefs()

		// Calculate system prompt length
		systemPromptLen := 0
		if len(messages) > 0 {
			if s, ok := messages[0].Content.(string); ok {
				systemPromptLen = len(s)
			}
		}

		// Log LLM request details
		logger.DebugCF("agent", "LLM request",
			map[string]interface{}{
				"iteration":         iteration,
				"model":             al.model,
				"messages_count":    len(messages),
				"tools_count":       len(providerToolDefs),
				"max_tokens":        8192,
				"temperature":       0.7,
				"system_prompt_len": systemPromptLen,
			})

		// Log full messages (detailed)
		logger.DebugCF("agent", "Full LLM request",
			map[string]interface{}{
				"iteration":     iteration,
				"messages_json": formatMessagesForLog(messages),
				"tools_json":    formatToolsForLog(providerToolDefs),
			})

		var response *providers.LLMResponse
		var err error

		// Track stream state for opencode-style events
		currentTextPartID := ""
		toolCallMap := make(map[string]string) // maps tool call ID to part ID

		// 如果启用流式模式，使用 ChatWithStream (opencode style)
		if opts.StreamMode {
			chatOpts := providers.ChatOptions{
				MaxTokens:   8192,
				Temperature: 0.7,
				// StreamCallback for text-delta events (1:1 from opencode)
				StreamCallback: func(chunk string, done bool) {
					if done {
						// Send text-end event (1:1 from opencode)
						if currentTextPartID != "" {
							al.sendTextEnd(opts, currentTextPartID)
							currentTextPartID = ""
						}
						return
					}

					if chunk != "" {
						// Send text-start event if not already started (1:1 from opencode)
						if currentTextPartID == "" {
							currentTextPartID = idGen.Next()
							al.sendTextStart(opts, currentTextPartID)
						}
						// Send text-delta event (1:1 from opencode)
						al.sendTextDelta(opts, currentTextPartID, chunk)
					}
				},
				// ToolCallCallback for tool events (1:1 from opencode)
				ToolCallCallback: func(toolName string, args map[string]interface{}) {
					// Tool call notification
					logger.DebugCF("agent", "Stream tool call initiated",
						map[string]interface{}{
							"tool": toolName,
						})
				},
			}

			response, err = al.provider.ChatWithStream(ctx, messages, providerToolDefs, al.model, chatOpts)

			// Ensure text-end is sent if we have an active text part
			if currentTextPartID != "" {
				al.sendTextEnd(opts, currentTextPartID)
				currentTextPartID = ""
			}
		} else {
			response, err = al.provider.Chat(ctx, messages, providerToolDefs, al.model, map[string]interface{}{
				"max_tokens":  8192,
				"temperature": 0.7,
			})
		}

		if err != nil {
			logger.ErrorCF("agent", "LLM call failed",
				map[string]interface{}{
					"iteration": iteration,
					"error":     err.Error(),
				})
			al.sendError(opts, fmt.Sprintf("LLM call failed: %v", err))
			return "", iteration, fmt.Errorf("LLM call failed: %w", err)
		}

		// Check if no tool calls - we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
				map[string]interface{}{
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})

			// Send finish-step event (1:1 from opencode)
			al.sendFinishStep(opts, iteration, response.FinishReason)
			break
		}

		// Log tool calls
		toolNames := make([]string, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("agent", "LLM requested tool calls",
			map[string]interface{}{
				"tools":     toolNames,
				"count":     len(response.ToolCalls),
				"iteration": iteration,
			})

		// Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:    "assistant",
			Content: response.Content,
		}
		for _, tc := range response.ToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: &providers.FunctionCall{
					Name:      tc.Name,
					Arguments: string(argumentsJSON),
				},
			})
		}
		messages = append(messages, assistantMsg)

		// Save assistant message with tool calls to session
		al.sessions.AddFullMessage(opts.SessionKey, assistantMsg)

		// Execute tool calls
		for _, tc := range response.ToolCalls {
			// Generate part ID for this tool call
			partID := idGen.Next()
			toolCallMap[tc.ID] = partID

			// Send tool-input-start event (1:1 from opencode)
			al.sendToolInputStart(opts, tc.ID, tc.Name, partID)

			// Send tool-call event (1:1 from opencode)
			al.sendToolCall(opts, tc.ID, tc.Name, tc.Arguments, iteration)

			logger.InfoCF("agent", fmt.Sprintf("Tool call: %s", tc.Name),
				map[string]interface{}{
					"tool":         tc.Name,
					"tool_call_id": tc.ID,
					"iteration":    iteration,
				})

			// Create async callback for tools that implement AsyncTool
			asyncCallback := func(callbackCtx context.Context, result *tools.ToolResult) {
				if !result.Silent && result.ForUser != "" {
					logger.InfoCF("agent", "Async tool completed, agent will handle notification",
						map[string]interface{}{
							"tool":        tc.Name,
							"content_len": len(result.ForUser),
						})
				}
			}

			toolResult := al.tools.ExecuteWithContext(ctx, tc.Name, tc.Arguments, opts.Channel, opts.ChatID, asyncCallback)

			// Determine content for LLM based on tool result
			contentForLLM := toolResult.ForLLM
			if contentForLLM == "" && toolResult.Err != nil {
				contentForLLM = toolResult.Err.Error()
			}

			// Send tool-result or tool-error event (1:1 from opencode)
			if toolResult.Err != nil {
				al.sendToolError(opts, tc.ID, tc.Name, toolResult.Err.Error(), iteration)
			} else {
				resultPreview := ""
				if toolResult.ForUser != "" {
					resultPreview = utils.Truncate(toolResult.ForUser, 500)
				} else if toolResult.ForLLM != "" {
					resultPreview = utils.Truncate(toolResult.ForLLM, 500)
				}
				al.sendToolResult(opts, tc.ID, tc.Name, resultPreview, iteration)
			}

			// Send ForUser content to user immediately if not Silent
			if !toolResult.Silent && toolResult.ForUser != "" && opts.SendResponse {
				al.bus.PublishOutbound(bus.OutboundMessage{
					Channel: opts.Channel,
					ChatID:  opts.ChatID,
					Content: toolResult.ForUser,
				})
				logger.DebugCF("agent", "Sent tool result to user",
					map[string]interface{}{
						"tool":        tc.Name,
						"content_len": len(toolResult.ForUser),
					})
			}

			toolResultMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolResultMsg)

			// Save tool result message to session
			al.sessions.AddFullMessage(opts.SessionKey, toolResultMsg)
		}

		// Send finish-step event (1:1 from opencode)
		al.sendFinishStep(opts, iteration, response.FinishReason)
	}

	return finalContent, iteration, nil
}

// updateToolContexts updates the context for tools that need channel/chatID info.
func (al *AgentLoop) updateToolContexts(channel, chatID string) {
	// Use ContextualTool interface instead of type assertions
	if tool, ok := al.tools.Get("message"); ok {
		if mt, ok := tool.(tools.ContextualTool); ok {
			mt.SetContext(channel, chatID)
		}
	}
	if tool, ok := al.tools.Get("spawn"); ok {
		if st, ok := tool.(tools.ContextualTool); ok {
			st.SetContext(channel, chatID)
		}
	}
	if tool, ok := al.tools.Get("subagent"); ok {
		if st, ok := tool.(tools.ContextualTool); ok {
			st.SetContext(channel, chatID)
		}
	}
}

// maybeSummarize triggers summarization if the session history exceeds thresholds.
func (al *AgentLoop) maybeSummarize(sessionKey string) {
	newHistory := al.sessions.GetHistory(sessionKey)
	tokenEstimate := al.estimateTokens(newHistory)
	threshold := al.contextWindow * 75 / 100

	if len(newHistory) > 20 || tokenEstimate > threshold {
		if _, loading := al.summarizing.LoadOrStore(sessionKey, true); !loading {
			go func() {
				defer al.summarizing.Delete(sessionKey)
				al.summarizeSession(sessionKey)
			}()
		}
	}
}

// GetStartupInfo returns information about loaded tools and skills for logging.
func (al *AgentLoop) GetStartupInfo() map[string]interface{} {
	info := make(map[string]interface{})

	// Tools info
	tools := al.tools.List()
	info["tools"] = map[string]interface{}{
		"count": len(tools),
		"names": tools,
	}

	// Skills info
	info["skills"] = al.contextBuilder.GetSkillsInfo()

	return info
}

// formatMessagesForLog formats messages for logging
func formatMessagesForLog(messages []providers.Message) string {
	if len(messages) == 0 {
		return "[]"
	}

	var result string
	result += "[\n"
	for i, msg := range messages {
		result += fmt.Sprintf("  [%d] Role: %s\n", i, msg.Role)
		if msg.ToolCalls != nil && len(msg.ToolCalls) > 0 {
			result += "  ToolCalls:\n"
			for _, tc := range msg.ToolCalls {
				result += fmt.Sprintf("    - ID: %s, Type: %s, Name: %s\n", tc.ID, tc.Type, tc.Name)
				if tc.Function != nil {
					result += fmt.Sprintf("      Arguments: %s\n", utils.Truncate(tc.Function.Arguments, 200))
				}
			}
		}
		if msg.Content != nil {
			contentStr := ""
			if s, ok := msg.Content.(string); ok {
				contentStr = s
			}
			if contentStr != "" {
				content := utils.Truncate(contentStr, 200)
				result += fmt.Sprintf("  Content: %s\n", content)
			}
		}
		if msg.ToolCallID != "" {
			result += fmt.Sprintf("  ToolCallID: %s\n", msg.ToolCallID)
		}
		result += "\n"
	}
	result += "]"
	return result
}

// formatToolsForLog formats tool definitions for logging
func formatToolsForLog(tools []providers.ToolDefinition) string {
	if len(tools) == 0 {
		return "[]"
	}

	var result string
	result += "[\n"
	for i, tool := range tools {
		result += fmt.Sprintf("  [%d] Type: %s, Name: %s\n", i, tool.Type, tool.Function.Name)
		result += fmt.Sprintf("      Description: %s\n", tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			result += fmt.Sprintf("      Parameters: %s\n", utils.Truncate(fmt.Sprintf("%v", tool.Function.Parameters), 200))
		}
	}
	result += "]"
	return result
}

// summarizeSession summarizes the conversation history for a session.
func (al *AgentLoop) summarizeSession(sessionKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	history := al.sessions.GetHistory(sessionKey)
	summary := al.sessions.GetSummary(sessionKey)

	// Keep last 4 messages for continuity
	if len(history) <= 4 {
		return
	}

	toSummarize := history[:len(history)-4]

	// Oversized Message Guard
	// Skip messages larger than 50% of context window to prevent summarizer overflow
	maxMessageTokens := al.contextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, m := range toSummarize {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		// Estimate tokens for this message
		contentLen := 0
		if s, ok := m.Content.(string); ok {
			contentLen = len(s)
		}
		msgTokens := contentLen / 4
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, m)
	}

	if len(validMessages) == 0 {
		return
	}

	// Multi-Part Summarization
	// Split into two parts if history is significant
	var finalSummary string
	if len(validMessages) > 10 {
		mid := len(validMessages) / 2
		part1 := validMessages[:mid]
		part2 := validMessages[mid:]

		s1, _ := al.summarizeBatch(ctx, part1, "")
		s2, _ := al.summarizeBatch(ctx, part2, "")

		// Merge them
		mergePrompt := fmt.Sprintf("Merge these two conversation summaries into one cohesive summary:\n\n1: %s\n\n2: %s", s1, s2)
		resp, err := al.provider.Chat(ctx, []providers.Message{{Role: "user", Content: mergePrompt}}, nil, al.model, map[string]interface{}{
			"max_tokens":  1024,
			"temperature": 0.3,
		})
		if err == nil {
			finalSummary = resp.Content
		} else {
			finalSummary = s1 + " " + s2
		}
	} else {
		finalSummary, _ = al.summarizeBatch(ctx, validMessages, summary)
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	if finalSummary != "" {
		al.sessions.SetSummary(sessionKey, finalSummary)
		al.sessions.TruncateHistory(sessionKey, 4)
		al.sessions.Save(sessionKey)
	}
}

// summarizeBatch summarizes a batch of messages.
func (al *AgentLoop) summarizeBatch(ctx context.Context, batch []providers.Message, existingSummary string) (string, error) {
	prompt := "Provide a concise summary of this conversation segment, preserving core context and key points.\n"
	if existingSummary != "" {
		prompt += "Existing context: " + existingSummary + "\n"
	}
	prompt += "\nCONVERSATION:\n"
	for _, m := range batch {
		contentStr := ""
		if s, ok := m.Content.(string); ok {
			contentStr = s
		}
		prompt += fmt.Sprintf("%s: %s\n", m.Role, contentStr)
	}

	response, err := al.provider.Chat(ctx, []providers.Message{{Role: "user", Content: prompt}}, nil, al.model, map[string]interface{}{
		"max_tokens":  1024,
		"temperature": 0.3,
	})
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// estimateTokens estimates the number of tokens in a message list.
// Uses rune count instead of byte length so that CJK and other multi-byte
// characters are not over-counted (a Chinese character is 3 bytes but roughly
// one token).
func (al *AgentLoop) estimateTokens(messages []providers.Message) int {
	total := 0
	for _, m := range messages {
		contentStr := ""
		if s, ok := m.Content.(string); ok {
			contentStr = s
		}
		total += utf8.RuneCountInString(contentStr) / 3
	}
	return total
}
