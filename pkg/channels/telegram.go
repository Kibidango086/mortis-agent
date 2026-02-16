package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/Kibidango086/mortis-agent/pkg/bus"
	"github.com/Kibidango086/mortis-agent/pkg/config"
	"github.com/Kibidango086/mortis-agent/pkg/logger"
	"github.com/Kibidango086/mortis-agent/pkg/utils"
	"github.com/Kibidango086/mortis-agent/pkg/voice"
)

// StreamState manages streaming message state
type StreamState struct {
	messageID    int
	chatID       int64
	mu           sync.RWMutex
	parts        map[string]*Part
	toolCalls    map[string]*Part
	toolCallList []string
	currentText  *Part
	iteration    int
	executionLog *bus.ExecutionLog
	isStreaming  bool
	lastUpdate   time.Time
}

type Part struct {
	ID         string
	Type       string // "text", "reasoning", "tool"
	Text       string
	ToolName   string
	ToolCallID string
	State      map[string]interface{}
}

func NewStreamState() *StreamState {
	return &StreamState{
		parts:        make(map[string]*Part),
		toolCalls:    make(map[string]*Part),
		toolCallList: make([]string, 0),
		lastUpdate:   time.Now(),
	}
}

func (s *StreamState) SetMessageID(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messageID = id
}

func (s *StreamState) GetMessageID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.messageID
}

func (s *StreamState) SetChatID(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatID = id
}

func (s *StreamState) GetChatID() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chatID
}

func (s *StreamState) SetActive(active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isStreaming = active
}

func (s *StreamState) AddPart(part *Part) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parts[part.ID] = part
}

func (s *StreamState) GetPart(id string) *Part {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.parts[id]
}

func (s *StreamState) SetCurrentText(part *Part) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentText = part
}

func (s *StreamState) UpdatePartDelta(partID string, delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if part, ok := s.parts[partID]; ok {
		part.Text += delta
		s.lastUpdate = time.Now()
	}
}

func (s *StreamState) AddToolCall(id string, part *Part) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls[id] = part
	s.toolCallList = append(s.toolCallList, id)
}

func (s *StreamState) GetToolCall(id string) *Part {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.toolCalls[id]
}

func (s *StreamState) AddExecutionStep(step bus.ExecutionStep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.executionLog == nil {
		s.executionLog = &bus.ExecutionLog{
			Steps:     []bus.ExecutionStep{},
			StartTime: time.Now(),
		}
	}
	s.executionLog.Steps = append(s.executionLog.Steps, step)
}

func (s *StreamState) GetExecutionLog() *bus.ExecutionLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.executionLog
}

func (s *StreamState) GetFinalText() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.currentText != nil && s.currentText.Text != "" {
		return s.currentText.Text
	}

	var finalText string
	for _, part := range s.parts {
		if part.Type == "text" && len(part.Text) > len(finalText) {
			finalText = part.Text
		}
	}
	return finalText
}

func (s *StreamState) GetDisplayContent() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var parts []string

	// Show iteration info
	if s.iteration > 0 {
		parts = append(parts, fmt.Sprintf("🔄 Step %d", s.iteration))
	}

	// Show tool calls - last 3
	if len(s.toolCalls) > 0 {
		var toolIDsToShow []string
		if len(s.toolCallList) <= 3 {
			toolIDsToShow = s.toolCallList
		} else {
			toolIDsToShow = s.toolCallList[len(s.toolCallList)-3:]
		}

		for _, toolID := range toolIDsToShow {
			tool := s.toolCalls[toolID]
			if tool == nil || tool.ToolName == "" {
				continue
			}

			var toolBlock strings.Builder
			toolHeader := fmt.Sprintf("🔧 %s", tool.ToolName)
			if status, ok := tool.State["status"].(string); ok {
				switch status {
				case "pending":
					toolHeader += " ⏳"
				case "running":
					toolHeader += " 🔄"
				case "completed":
					toolHeader += " ✅"
				case "error":
					toolHeader += " ❌"
				}
			}
			toolBlock.WriteString(toolHeader)
			toolBlock.WriteString("\n")

			if input, ok := tool.State["input"].(map[string]interface{}); ok && len(input) > 0 {
				argsJSON, _ := json.MarshalIndent(input, "", "  ")
				argsStr := string(argsJSON)
				maxInputLen := 200
				if len(argsStr) > maxInputLen {
					argsStr = argsStr[:maxInputLen] + "..."
				}
				toolBlock.WriteString("```\nInput:\n")
				toolBlock.WriteString(argsStr)
				toolBlock.WriteString("\n```")
			}

			if output, ok := tool.State["output"].(string); ok && output != "" {
				maxOutputLen := 150
				displayOutput := output
				if len(output) > maxOutputLen {
					displayOutput = output[:maxOutputLen] + "..."
				}
				toolBlock.WriteString("```\nOutput:\n")
				toolBlock.WriteString(displayOutput)
				toolBlock.WriteString("\n```")
				if len(output) > maxOutputLen {
					toolBlock.WriteString("\n_查看完整结果请使用 View Details_")
				}
			}

			if errStr, ok := tool.State["error"].(string); ok && errStr != "" {
				maxErrorLen := 200
				displayError := errStr
				if len(errStr) > maxErrorLen {
					displayError = errStr[:maxErrorLen] + "..."
				}
				toolBlock.WriteString("```\nError:\n")
				toolBlock.WriteString(displayError)
				toolBlock.WriteString("\n```")
			}

			parts = append(parts, toolBlock.String())
		}

		if len(s.toolCallList) > 3 {
			parts = append(parts, fmt.Sprintf("📋 ... and %d more", len(s.toolCallList)-3))
		}
	}

	// Show final text
	finalText := ""
	if s.currentText != nil && s.currentText.Text != "" {
		finalText = s.currentText.Text
	} else {
		for _, part := range s.parts {
			if part.Type == "text" && len(part.Text) > len(finalText) {
				finalText = part.Text
			}
		}
	}

	if finalText != "" {
		if len(parts) > 0 {
			parts = append(parts, "")
		}
		parts = append(parts, finalText)
	}

	return strings.Join(parts, "\n")
}

// TelegramChannel implements Telegram bot integration
type TelegramChannel struct {
	*BaseChannel
	bot          *telego.Bot
	config       config.TelegramConfig
	chatIDs      map[string]int64
	transcriber  *voice.GroqTranscriber
	streamStates sync.Map
	mu           sync.RWMutex
}

// ExecutionContext stores execution log for View Details feature
type ExecutionContext struct {
	Log          *bus.ExecutionLog
	FinalContent string
	ChatID       int64
	MessageID    int
}

var executionLogStore = struct {
	sync.RWMutex
	logs map[string]*ExecutionContext
}{
	logs: make(map[string]*ExecutionContext),
}

func NewTelegramChannel(cfg config.TelegramConfig, bus *bus.MessageBus) (*TelegramChannel, error) {
	var opts []telego.BotOption

	if cfg.Proxy != "" {
		proxyURL, parseErr := url.Parse(cfg.Proxy)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", cfg.Proxy, parseErr)
		}
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}))
	}

	bot, err := telego.NewBot(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	base := NewBaseChannel("telegram", cfg, bus, cfg.AllowFrom)

	return &TelegramChannel{
		BaseChannel: base,
		bot:         bot,
		config:      cfg,
		chatIDs:     make(map[string]int64),
	}, nil
}

func (c *TelegramChannel) SetTranscriber(transcriber *voice.GroqTranscriber) {
	c.transcriber = transcriber
}

func (c *TelegramChannel) Start(ctx context.Context) error {
	logger.InfoC("telegram", "Starting Telegram bot...")

	updates, err := c.bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		Timeout: 30,
	})
	if err != nil {
		return fmt.Errorf("failed to start long polling: %w", err)
	}

	c.setRunning(true)
	logger.InfoCF("telegram", "Telegram bot connected", map[string]interface{}{
		"username": c.bot.Username(),
	})

	go c.handleStreamMessages(ctx)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updates:
				if !ok {
					logger.InfoC("telegram", "Updates channel closed, reconnecting...")
					return
				}
				if update.Message != nil {
					c.handleMessage(ctx, update)
				} else if update.CallbackQuery != nil {
					c.handleCallbackQuery(ctx, update)
				}
			}
		}
	}()

	return nil
}

func (c *TelegramChannel) Stop(ctx context.Context) error {
	logger.InfoC("telegram", "Stopping Telegram bot...")
	c.setRunning(false)
	return nil
}

func (c *TelegramChannel) handleStreamMessages(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			msg, ok := c.bus.SubscribeStream(ctx)
			if !ok {
				continue
			}

			if msg.Channel != "telegram" {
				continue
			}

			c.handleStreamEvent(ctx, msg)
		}
	}
}

func (c *TelegramChannel) handleStreamEvent(ctx context.Context, msg bus.StreamMessage) {
	if msg.SessionKey == "heartbeat" {
		return
	}

	chatID, err := parseChatID(msg.ChatID)
	if err != nil {
		logger.ErrorCF("telegram", "Invalid chat ID", map[string]interface{}{"chat_id": msg.ChatID})
		return
	}

	stateInterface, _ := c.streamStates.LoadOrStore(msg.ChatID, NewStreamState())
	state := stateInterface.(*StreamState)

	switch msg.Type {
	case bus.StreamEventTextStart:
		part := &Part{
			ID:   msg.PartID,
			Type: "text",
			Text: "",
		}
		state.AddPart(part)
		state.SetCurrentText(part)
		state.SetActive(true)

	case bus.StreamEventTextDelta:
		state.UpdatePartDelta(msg.PartID, msg.Delta)
		if time.Since(state.lastUpdate) > 500*time.Millisecond {
			c.updateStreamMessage(ctx, chatID, state)
		}

	case bus.StreamEventTextEnd:
		if part := state.GetPart(msg.PartID); part != nil {
			part.Text = strings.TrimRight(part.Text, " \n\t")
		}
		state.SetCurrentText(nil)
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventToolInputStart:
		part := &Part{
			ID:         msg.PartID,
			Type:       "tool",
			ToolName:   msg.ToolName,
			ToolCallID: msg.ToolCallID,
			State: map[string]interface{}{
				"status": "pending",
				"input":  map[string]interface{}{},
			},
		}
		state.AddPart(part)
		state.AddToolCall(msg.ToolCallID, part)
		state.AddExecutionStep(bus.ExecutionStep{
			Type:      "tool_call",
			Timestamp: time.Now(),
			ToolName:  msg.ToolName,
			Iteration: msg.Iteration,
		})
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventToolCall:
		if part := state.GetToolCall(msg.ToolCallID); part != nil {
			part.ToolName = msg.ToolName
			part.State["status"] = "running"
			part.State["input"] = msg.ToolArgs
		}
		state.AddExecutionStep(bus.ExecutionStep{
			Type:      "tool_call",
			Timestamp: time.Now(),
			ToolName:  msg.ToolName,
			ToolArgs:  msg.ToolArgs,
			Iteration: msg.Iteration,
		})
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventToolResult:
		if part := state.GetToolCall(msg.ToolCallID); part != nil {
			part.State["status"] = "completed"
			part.State["output"] = msg.ToolResult
		}
		state.AddExecutionStep(bus.ExecutionStep{
			Type:       "tool_result",
			Timestamp:  time.Now(),
			ToolName:   msg.ToolName,
			ToolResult: msg.ToolResult,
			Iteration:  msg.Iteration,
		})
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventToolError:
		if part := state.GetToolCall(msg.ToolCallID); part != nil {
			part.State["status"] = "error"
			part.State["error"] = msg.Error
		}
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventStartStep:
		state.iteration = msg.Iteration

	case bus.StreamEventFinish:
		state.SetActive(false)
		c.finalizeStreamMessage(ctx, chatID, state, msg.SessionKey)
		c.streamStates.Delete(msg.ChatID)

	case bus.StreamEventError:
		state.SetActive(false)
		c.sendErrorMessage(ctx, chatID, msg.Content)
		c.streamStates.Delete(msg.ChatID)
	}
}

func (c *TelegramChannel) updateStreamMessage(ctx context.Context, chatID int64, state *StreamState) {
	content := state.GetDisplayContent()
	if content == "" {
		return
	}

	htmlContent := markdownToTelegramHTML(content)

	// Truncate if too long
	const maxLength = 4000
	if len(htmlContent) > maxLength {
		htmlContent = htmlContent[:maxLength] + "\n\n<i>[Message truncated]</i>"
	}

	messageID := state.GetMessageID()
	if messageID != 0 {
		editMsg := tu.EditMessageText(tu.ID(chatID), messageID, htmlContent)
		editMsg.ParseMode = telego.ModeHTML
		if _, err := c.bot.EditMessageText(ctx, editMsg); err != nil {
			c.sendNewStreamMessage(ctx, chatID, state, htmlContent)
		}
	} else {
		c.sendNewStreamMessage(ctx, chatID, state, htmlContent)
	}
}

func (c *TelegramChannel) sendNewStreamMessage(ctx context.Context, chatID int64, state *StreamState, htmlContent string) {
	msg := tu.Message(tu.ID(chatID), htmlContent)
	msg.ParseMode = telego.ModeHTML

	sentMsg, err := c.bot.SendMessage(ctx, msg)
	if err != nil {
		msg.ParseMode = ""
		sentMsg, err = c.bot.SendMessage(ctx, msg)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to send message", map[string]interface{}{"error": err.Error()})
			return
		}
	}

	state.SetMessageID(sentMsg.MessageID)
	state.SetChatID(chatID)
}

func (c *TelegramChannel) finalizeStreamMessage(ctx context.Context, chatID int64, state *StreamState, sessionKey string) {
	execLog := state.GetExecutionLog()
	if execLog != nil {
		execLog.EndTime = time.Now()
	}

	hasToolCalls := len(state.toolCalls) > 0
	hasExecutionSteps := execLog != nil && len(execLog.Steps) > 0
	messageID := state.GetMessageID()
	finalContent := state.GetFinalText()

	logger.InfoCF("telegram", "Finalizing stream", map[string]interface{}{
		"session_key":         sessionKey,
		"has_tool_calls":      hasToolCalls,
		"has_execution_steps": hasExecutionSteps,
		"message_id":          messageID,
	})

	if hasToolCalls || hasExecutionSteps {
		// Create steps from tool calls if needed
		if execLog == nil {
			execLog = &bus.ExecutionLog{
				Steps:     []bus.ExecutionStep{},
				StartTime: time.Now(),
			}
		}

		if len(execLog.Steps) == 0 && hasToolCalls {
			for _, toolID := range state.toolCallList {
				tool := state.toolCalls[toolID]
				if tool == nil || tool.ToolName == "" {
					continue
				}
				step := bus.ExecutionStep{
					Type:      "tool_call",
					Timestamp: time.Now(),
					ToolName:  tool.ToolName,
				}
				if input, ok := tool.State["input"].(map[string]interface{}); ok {
					step.ToolArgs = input
				}
				if output, ok := tool.State["output"].(string); ok {
					step.ToolResult = output
				}
				execLog.Steps = append(execLog.Steps, step)
			}
		}

		executionLogStore.Lock()
		executionLogStore.logs[sessionKey] = &ExecutionContext{
			Log:          execLog,
			FinalContent: finalContent,
			ChatID:       chatID,
			MessageID:    messageID,
		}
		executionLogStore.Unlock()

		// Edit final message to add View Details button
		if messageID != 0 {
			finalHTML := markdownToTelegramHTML(finalContent)
			const maxLength = 4000
			if len(finalHTML) > maxLength {
				finalHTML = finalHTML[:maxLength] + "\n\n<i>[Message truncated]</i>"
			}
			if finalHTML == "" {
				finalHTML = "✅ Completed"
			}

			editMsg := tu.EditMessageText(tu.ID(chatID), messageID, finalHTML)
			editMsg.ParseMode = telego.ModeHTML
			editMsg.ReplyMarkup = tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					tu.InlineKeyboardButton("📋 View Details").WithCallbackData(fmt.Sprintf("view_log:%s", sessionKey)),
				),
			)
			if _, err := c.bot.EditMessageText(ctx, editMsg); err != nil {
				logger.WarnCF("telegram", "Failed to add View Details button to final message", map[string]interface{}{
					"error": err.Error(),
				})
			}
		}
	}
}

func (c *TelegramChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return fmt.Errorf("telegram bot not running")
	}

	if msg.SessionID == "heartbeat" {
		return nil
	}

	chatID, err := parseChatID(msg.ChatID)
	if err != nil {
		return fmt.Errorf("invalid chat ID: %w", err)
	}

	// Send media files first
	if len(msg.Media) > 0 {
		c.sendMediaFiles(ctx, chatID, msg.Media, msg.SessionID)
	}

	if msg.Content == "" {
		return nil
	}

	// Get execution context for View Details button
	executionLogStore.RLock()
	execCtx, exists := executionLogStore.logs[msg.SessionID]
	executionLogStore.RUnlock()

	finalContent := markdownToTelegramHTML(msg.Content)

	// Truncate if too long
	const maxLength = 4000
	if len(finalContent) > maxLength {
		finalContent = finalContent[:maxLength] + "\n\n<i>[Message truncated]</i>"
	}

	if exists && execCtx != nil && execCtx.MessageID != 0 {
		// Edit existing message with final content only
		hasToolCalls := execCtx.Log != nil && len(execCtx.Log.Steps) > 0

		editMsg := tu.EditMessageText(tu.ID(chatID), execCtx.MessageID, finalContent)
		editMsg.ParseMode = telego.ModeHTML

		if hasToolCalls {
			editMsg.ReplyMarkup = tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					tu.InlineKeyboardButton("📋 View Details").WithCallbackData(fmt.Sprintf("view_log:%s", msg.SessionID)),
				),
			)
		}

		_, editErr := c.bot.EditMessageText(ctx, editMsg)
		if editErr == nil {
			logger.InfoCF("telegram", "Edited message with final result", map[string]interface{}{
				"session_id": msg.SessionID,
				"has_button": hasToolCalls,
			})
			// Clean up
			executionLogStore.Lock()
			delete(executionLogStore.logs, msg.SessionID)
			executionLogStore.Unlock()
			return nil
		}

		// Edit failed, try sending new message
		logger.WarnCF("telegram", "Edit failed, sending new message", map[string]interface{}{
			"error": editErr.Error(),
		})
	}

	// Send as new message
	tgMsg := tu.Message(tu.ID(chatID), finalContent)
	tgMsg.ParseMode = telego.ModeHTML

	if exists && execCtx != nil && execCtx.Log != nil && len(execCtx.Log.Steps) > 0 {
		tgMsg.ReplyMarkup = tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				tu.InlineKeyboardButton("📋 View Details").WithCallbackData(fmt.Sprintf("view_log:%s", msg.SessionID)),
			),
		)
	}

	if _, err := c.bot.SendMessage(ctx, tgMsg); err != nil {
		tgMsg.ParseMode = ""
		_, err = c.bot.SendMessage(ctx, tgMsg)
		if err != nil {
			return err
		}
	}

	// Clean up execution context
	if exists {
		executionLogStore.Lock()
		delete(executionLogStore.logs, msg.SessionID)
		executionLogStore.Unlock()
	}

	return nil
}

func (c *TelegramChannel) handleCallbackQuery(ctx context.Context, update telego.Update) {
	if update.CallbackQuery == nil {
		return
	}

	callback := update.CallbackQuery
	data := callback.Data

	if strings.HasPrefix(data, "view_log:") {
		sessionKey := strings.TrimPrefix(data, "view_log:")

		executionLogStore.RLock()
		execCtx, exists := executionLogStore.logs[sessionKey]
		executionLogStore.RUnlock()

		if !exists || execCtx == nil || execCtx.Log == nil {
			c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
				CallbackQueryID: callback.ID,
				Text:            "Details not available",
				ShowAlert:       true,
			})
			return
		}

		// Show tool calls
		c.showToolCallsPage(ctx, callback, sessionKey, execCtx, 0)
		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
		})
	} else if strings.HasPrefix(data, "tools_page:") {
		parts := strings.Split(data, ":")
		if len(parts) < 3 {
			return
		}
		sessionKey := parts[1]
		pageNum := 0
		fmt.Sscanf(parts[2], "%d", &pageNum)

		executionLogStore.RLock()
		execCtx, exists := executionLogStore.logs[sessionKey]
		executionLogStore.RUnlock()

		if !exists || execCtx == nil {
			return
		}

		c.showToolCallsPage(ctx, callback, sessionKey, execCtx, pageNum)
		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
		})
	} else if strings.HasPrefix(data, "back_result:") {
		sessionKey := strings.TrimPrefix(data, "back_result:")

		executionLogStore.RLock()
		execCtx, exists := executionLogStore.logs[sessionKey]
		executionLogStore.RUnlock()

		if !exists || execCtx == nil {
			return
		}

		finalContent := markdownToTelegramHTML(execCtx.FinalContent)
		if finalContent == "" {
			finalContent = "✅ Completed"
		}

		c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: callback.Message.GetChat().ID},
			MessageID: callback.Message.GetMessageID(),
			Text:      finalContent,
			ParseMode: telego.ModeHTML,
			ReplyMarkup: tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					tu.InlineKeyboardButton("📋 View Details").WithCallbackData(fmt.Sprintf("view_log:%s", sessionKey)),
				),
			),
		})

		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
		})
	}
}

func (c *TelegramChannel) showToolCallsPage(ctx context.Context, callback *telego.CallbackQuery, sessionKey string, execCtx *ExecutionContext, pageNum int) {
	if execCtx.Log == nil || len(execCtx.Log.Steps) == 0 {
		return
	}

	var toolCalls []bus.ExecutionStep
	for _, step := range execCtx.Log.Steps {
		if step.Type == "tool_call" || step.Type == "tool_result" {
			toolCalls = append(toolCalls, step)
		}
	}

	if len(toolCalls) == 0 {
		return
	}

	toolsPerPage := 2
	totalPages := (len(toolCalls) + toolsPerPage - 1) / toolsPerPage

	if pageNum < 0 {
		pageNum = 0
	}
	if pageNum >= totalPages {
		pageNum = totalPages - 1
	}

	startIdx := pageNum * toolsPerPage
	endIdx := startIdx + toolsPerPage
	if endIdx > len(toolCalls) {
		endIdx = len(toolCalls)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 Tool Calls (Page %d/%d)\n\n", pageNum+1, totalPages))
	sb.WriteString(strings.Repeat("─", 32))
	sb.WriteString("\n\n")

	pageTools := toolCalls[startIdx:endIdx]
	for i, tool := range pageTools {
		displayNum := startIdx + i + 1
		sb.WriteString(fmt.Sprintf("%d. 🔧 %s\n", displayNum, tool.ToolName))

		if len(tool.ToolArgs) > 0 {
			argsJSON, _ := json.MarshalIndent(tool.ToolArgs, "", "  ")
			argsStr := string(argsJSON)
			if len(argsStr) > 300 {
				argsStr = argsStr[:297] + "..."
			}
			sb.WriteString("   Input:\n")
			for _, line := range strings.Split(argsStr, "\n") {
				sb.WriteString("   ")
				sb.WriteString(line)
				sb.WriteString("\n")
			}
		}

		if tool.ToolResult != "" {
			result := tool.ToolResult
			if len(result) > 300 {
				result = result[:297] + "..."
			}
			sb.WriteString("   Output:\n")
			for _, line := range strings.Split(result, "\n") {
				sb.WriteString("   ")
				sb.WriteString(line)
				sb.WriteString("\n")
			}
		}

		sb.WriteString("\n")
	}

	content := sb.String()
	escapedContent := escapeHTMLForTelegram(content)

	var navButtons []telego.InlineKeyboardButton
	if pageNum > 0 {
		navButtons = append(navButtons, tu.InlineKeyboardButton("⬅️ Prev").WithCallbackData(fmt.Sprintf("tools_page:%s:%d", sessionKey, pageNum-1)))
	}
	navButtons = append(navButtons, tu.InlineKeyboardButton("◀️ Back").WithCallbackData(fmt.Sprintf("back_result:%s", sessionKey)))
	if pageNum < totalPages-1 {
		navButtons = append(navButtons, tu.InlineKeyboardButton("Next ➡️").WithCallbackData(fmt.Sprintf("tools_page:%s:%d", sessionKey, pageNum+1)))
	}

	keyboard := tu.InlineKeyboard(tu.InlineKeyboardRow(navButtons...))

	c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:      telego.ChatID{ID: callback.Message.GetChat().ID},
		MessageID:   callback.Message.GetMessageID(),
		Text:        escapedContent,
		ParseMode:   "",
		ReplyMarkup: keyboard,
	})
}

func escapeHTMLForTelegram(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

func (c *TelegramChannel) handleMessage(ctx context.Context, update telego.Update) {
	message := update.Message
	if message == nil {
		return
	}

	user := message.From
	if user == nil {
		return
	}

	userID := fmt.Sprintf("%d", user.ID)
	senderID := userID
	if user.Username != "" {
		senderID = fmt.Sprintf("%s|%s", userID, user.Username)
	}

	if !c.IsAllowed(userID) && !c.IsAllowed(senderID) {
		return
	}

	chatID := message.Chat.ID
	c.mu.Lock()
	c.chatIDs[senderID] = chatID
	c.mu.Unlock()

	content := ""
	mediaPaths := []string{}

	if message.Text != "" {
		content += message.Text
	}

	if message.Caption != "" {
		if content != "" {
			content += "\n"
		}
		content += message.Caption
	}

	if message.Photo != nil && len(message.Photo) > 0 {
		photo := message.Photo[len(message.Photo)-1]
		photoPath := c.downloadPhoto(ctx, photo.FileID)
		if photoPath != "" {
			mediaPaths = append(mediaPaths, photoPath)
			if content != "" {
				content += "\n"
			}
			content += "[image: photo]"
		}
	}

	if message.Voice != nil {
		voicePath := c.downloadFile(ctx, message.Voice.FileID, ".ogg")
		if voicePath != "" {
			mediaPaths = append(mediaPaths, voicePath)
			transcribedText := ""
			if c.transcriber != nil && c.transcriber.IsAvailable() {
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				result, err := c.transcriber.Transcribe(ctx, voicePath)
				if err != nil {
					transcribedText = "[voice (transcription failed)]"
				} else {
					transcribedText = fmt.Sprintf("[voice transcription: %s]", result.Text)
				}
			} else {
				transcribedText = "[voice]"
			}
			if content != "" {
				content += "\n"
			}
			content += transcribedText
		}
	}

	if message.Audio != nil {
		audioPath := c.downloadFile(ctx, message.Audio.FileID, ".mp3")
		if audioPath != "" {
			mediaPaths = append(mediaPaths, audioPath)
			if content != "" {
				content += "\n"
			}
			content += "[audio]"
		}
	}

	if message.Document != nil {
		docPath := c.downloadFile(ctx, message.Document.FileID, "")
		if docPath != "" {
			mediaPaths = append(mediaPaths, docPath)
			if content != "" {
				content += "\n"
			}
			content += "[file]"
		}
	}

	if content == "" {
		content = "[empty message]"
	}

	c.bot.SendChatAction(ctx, tu.ChatAction(tu.ID(chatID), telego.ChatActionTyping))

	// Initialize stream state
	stateInterface, _ := c.streamStates.LoadOrStore(fmt.Sprintf("%d", chatID), NewStreamState())
	state := stateInterface.(*StreamState)
	state.SetChatID(chatID)

	metadata := map[string]string{
		"message_id": fmt.Sprintf("%d", message.MessageID),
		"user_id":    fmt.Sprintf("%d", user.ID),
		"username":   user.Username,
		"first_name": user.FirstName,
		"is_group":   fmt.Sprintf("%t", message.Chat.Type != "private"),
	}

	sessionKey := fmt.Sprintf("%s:%s", c.Name(), fmt.Sprintf("%d", chatID))

	msg := bus.InboundMessage{
		Channel:    c.Name(),
		SenderID:   senderID,
		ChatID:     fmt.Sprintf("%d", chatID),
		Content:    content,
		Media:      mediaPaths,
		SessionKey: sessionKey,
		Metadata:   metadata,
		StreamMode: c.config.StreamMode,
	}

	c.bus.PublishInbound(msg)
}

func (c *TelegramChannel) sendMediaFiles(ctx context.Context, chatID int64, mediaPaths []string, sessionID string) error {
	if len(mediaPaths) == 0 {
		return nil
	}

	var images []string
	var documents []string

	for _, path := range mediaPaths {
		mimeType := getMimeType(path)
		if strings.HasPrefix(mimeType, "image/") {
			images = append(images, path)
		} else {
			documents = append(documents, path)
		}
	}

	if len(images) > 0 {
		if err := c.sendImages(ctx, chatID, images); err != nil {
			logger.ErrorCF("telegram", "Failed to send images", map[string]interface{}{"error": err.Error()})
		}
	}

	for _, docPath := range documents {
		if err := c.sendDocument(ctx, chatID, docPath); err != nil {
			logger.ErrorCF("telegram", "Failed to send document", map[string]interface{}{"error": err.Error()})
		}
	}

	return nil
}

func (c *TelegramChannel) sendImages(ctx context.Context, chatID int64, imagePaths []string) error {
	if len(imagePaths) == 0 {
		return nil
	}

	if len(imagePaths) == 1 {
		file, err := os.Open(imagePaths[0])
		if err != nil {
			return err
		}
		defer file.Close()

		photo := tu.Photo(tu.ID(chatID), tu.File(file))
		_, err = c.bot.SendPhoto(ctx, photo)
		return err
	}

	var mediaGroup []telego.InputMedia
	for i, path := range imagePaths {
		if i >= 10 {
			break
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		defer file.Close()

		media := &telego.InputMediaPhoto{
			Type:  "photo",
			Media: telego.InputFile{File: file},
		}
		mediaGroup = append(mediaGroup, media)
	}

	if len(mediaGroup) == 0 {
		return fmt.Errorf("no valid images")
	}

	_, err := c.bot.SendMediaGroup(ctx, &telego.SendMediaGroupParams{
		ChatID: telego.ChatID{ID: chatID},
		Media:  mediaGroup,
	})

	return err
}

func (c *TelegramChannel) sendDocument(ctx context.Context, chatID int64, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	doc := tu.Document(tu.ID(chatID), tu.File(file))
	_, err = c.bot.SendDocument(ctx, doc)
	return err
}

func (c *TelegramChannel) sendErrorMessage(ctx context.Context, chatID int64, errorMsg string) {
	htmlContent := markdownToTelegramHTML(fmt.Sprintf("❌ Error: %s", errorMsg))
	msg := tu.Message(tu.ID(chatID), htmlContent)
	msg.ParseMode = telego.ModeHTML
	c.bot.SendMessage(ctx, msg)
}

func (c *TelegramChannel) downloadPhoto(ctx context.Context, fileID string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return ""
	}
	return c.downloadFileWithInfo(file, ".jpg")
}

func (c *TelegramChannel) downloadFile(ctx context.Context, fileID, ext string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return ""
	}
	return c.downloadFileWithInfo(file, ext)
}

func (c *TelegramChannel) downloadFileWithInfo(file *telego.File, ext string) string {
	if file.FilePath == "" {
		return ""
	}
	url := c.bot.FileDownloadURL(file.FilePath)
	filename := file.FilePath + ext
	return utils.DownloadFile(url, filename, utils.DownloadOptions{LoggerPrefix: "telegram"})
}

func parseChatID(chatIDStr string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(chatIDStr, "%d", &id)
	return id, err
}

func getMimeType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	// Process code blocks first
	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	// Convert markdown
	text = regexp.MustCompile(`^#{1,6}\s+(.+)$`).ReplaceAllString(text, "<b>$1</b>")
	text = regexp.MustCompile(`^>\s*(.*)$`).ReplaceAllString(text, "<i>$1</i>")
	text = regexp.MustCompile(`^---+\s*$`).ReplaceAllString(text, "─"+strings.Repeat("─", 30))

	text = escapeHTML(text)

	text = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`).ReplaceAllString(text, `<a href="$2">$1</a>`)
	text = regexp.MustCompile(`\*\*(.+?)\*\*`).ReplaceAllString(text, "<b>$1</b>")
	text = regexp.MustCompile(`__(.+?)__`).ReplaceAllString(text, "<b>$1</b>")
	text = regexp.MustCompile(`(^|[^\*])\*([^\*]+?)\*([^\*]|$)`).ReplaceAllString(text, "$1<i>$2</i>$3")
	text = regexp.MustCompile(`(^|[^_])_([^_]+?)_([^_]|$)`).ReplaceAllString(text, "$1<i>$2</i>$3")
	text = regexp.MustCompile(`~~(.+?)~~`).ReplaceAllString(text, "<s>$1</s>")
	text = regexp.MustCompile(`(?m)^[-*]\s+(.+)$`).ReplaceAllString(text, "• $1")
	text = regexp.MustCompile(`(?m)^(\d+)\.\s+(.+)$`).ReplaceAllString(text, "$1. $2")

	// Restore inline codes
	for i, code := range inlineCodes.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00IC%d\x00", i), fmt.Sprintf("<code>%s</code>", escaped))
	}

	// Restore code blocks
	for i, code := range codeBlocks.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00CB%d\x00", i), fmt.Sprintf("<pre><code>%s</code></pre>", escaped))
	}

	text = strings.ReplaceAll(text, "\n\n", "\n")

	return text
}

type codeBlockMatch struct {
	text  string
	codes []string
}

func extractCodeBlocks(text string) codeBlockMatch {
	re := regexp.MustCompile("```[\\w]*\\n?([\\s\\S]*?)```")
	matches := re.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = re.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00CB%d\x00", i)
		i++
		return placeholder
	})

	return codeBlockMatch{text: text, codes: codes}
}

type inlineCodeMatch struct {
	text  string
	codes []string
}

func extractInlineCodes(text string) inlineCodeMatch {
	re := regexp.MustCompile("`([^`]+)`")
	matches := re.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = re.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00IC%d\x00", i)
		i++
		return placeholder
	})

	return inlineCodeMatch{text: text, codes: codes}
}

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}
