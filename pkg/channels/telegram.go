package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// Part 表示消息的一个部分，1:1 对应 opencode 的 part 概念
type Part struct {
	ID         string
	Type       string // "text", "reasoning", "tool"
	Text       string
	ToolName   string
	ToolCallID string
	State      map[string]interface{}
	Time       struct {
		Start int64
		End   int64
	}
}

// StreamState 管理流式状态，1:1 还原 opencode 的流式处理
type StreamState struct {
	messageID int
	chatID    int64
	mu        sync.RWMutex

	// Parts 管理 - 1:1 from opencode
	parts        map[string]*Part
	currentText  *Part
	reasoningMap map[string]*Part
	toolCalls    map[string]*Part

	// Execution tracking
	iteration    int
	executionLog *bus.ExecutionLog

	// Display state
	displayContent string
	lastUpdate     time.Time
	isStreaming    bool
}

func NewStreamState() *StreamState {
	return &StreamState{
		parts:        make(map[string]*Part),
		reasoningMap: make(map[string]*Part),
		toolCalls:    make(map[string]*Part),
		lastUpdate:   time.Now(),
	}
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

func (s *StreamState) UpdatePartDelta(partID string, delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if part, ok := s.parts[partID]; ok {
		part.Text += delta
		s.lastUpdate = time.Now()
	}
}

func (s *StreamState) SetCurrentText(part *Part) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentText = part
}

func (s *StreamState) GetCurrentText() *Part {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentText
}

func (s *StreamState) AddReasoning(id string, part *Part) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reasoningMap[id] = part
}

func (s *StreamState) GetReasoning(id string) *Part {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reasoningMap[id]
}

func (s *StreamState) UpdateReasoningDelta(id string, delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if part, ok := s.reasoningMap[id]; ok {
		part.Text += delta
		s.lastUpdate = time.Now()
	}
}

func (s *StreamState) RemoveReasoning(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reasoningMap, id)
}

func (s *StreamState) AddToolCall(id string, part *Part) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls[id] = part
}

func (s *StreamState) GetToolCall(id string) *Part {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.toolCalls[id]
}

func (s *StreamState) RemoveToolCall(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.toolCalls, id)
}

func (s *StreamState) GetDisplayContent() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var parts []string

	// Show iteration info if in multi-step process
	if s.iteration > 0 {
		parts = append(parts, fmt.Sprintf("🔄 Step %d", s.iteration))
	}

	// Show reasoning first (if any)
	for _, reasoning := range s.reasoningMap {
		if reasoning.Text != "" {
			parts = append(parts, fmt.Sprintf("💭 %s", reasoning.Text))
		}
	}

	// Show active/completed tool calls with details (like opencode)
	if len(s.toolCalls) > 0 {
		for _, tool := range s.toolCalls {
			if tool.ToolName != "" {
				toolLine := fmt.Sprintf("🔧 %s", tool.ToolName)

				// Add status indicator
				if status, ok := tool.State["status"].(string); ok {
					switch status {
					case "pending":
						toolLine += " ⏳"
					case "running":
						toolLine += " 🔄"
					case "completed":
						toolLine += " ✅"
					case "error":
						toolLine += " ❌"
					}
				}

				// Add input args preview
				if input, ok := tool.State["input"].(map[string]interface{}); ok && len(input) > 0 {
					argsPreview := formatArgsPreview(input)
					if argsPreview != "" {
						toolLine += fmt.Sprintf("(%s)", argsPreview)
					}
				}

				parts = append(parts, toolLine)

				// Show result preview if completed
				if output, ok := tool.State["output"].(string); ok && output != "" {
					preview := truncateString(output, 100)
					parts = append(parts, fmt.Sprintf("   → %s", preview))
				}

				// Show error if any
				if err, ok := tool.State["error"].(string); ok && err != "" {
					parts = append(parts, fmt.Sprintf("   ⚠️ %s", truncateString(err, 100)))
				}
			}
		}
	}

	// Show main text content
	if s.currentText != nil && s.currentText.Text != "" {
		if len(parts) > 0 {
			parts = append(parts, "")
		}
		parts = append(parts, s.currentText.Text)
	}

	return strings.Join(parts, "\n")
}

// formatArgsPreview formats tool arguments for display
func formatArgsPreview(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}

	var previewParts []string
	for key, value := range args {
		valueStr := fmt.Sprintf("%v", value)
		// Truncate long values
		if len(valueStr) > 30 {
			valueStr = valueStr[:27] + "..."
		}
		previewParts = append(previewParts, fmt.Sprintf("%s=%s", key, valueStr))
	}

	return strings.Join(previewParts, ", ")
}

// truncateString truncates a string to max length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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

func (s *StreamState) IsActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isStreaming
}

func (s *StreamState) SetActive(active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isStreaming = active
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

// TelegramChannel Telegram 通道实现
type TelegramChannel struct {
	*BaseChannel
	bot          *telego.Bot
	config       config.TelegramConfig
	chatIDs      map[string]int64
	transcriber  *voice.GroqTranscriber
	streamStates sync.Map // chatID -> *StreamState
	mu           sync.RWMutex
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
	logger.InfoC("telegram", "Starting Telegram bot (opencode full-text mode)...")

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

	// 启动流式消息处理 goroutine
	go c.handleStreamMessages(ctx)

	// 处理更新
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

// handleStreamEvent 1:1 还原 opencode 的流式事件处理逻辑
func (c *TelegramChannel) handleStreamEvent(ctx context.Context, msg bus.StreamMessage) {
	chatID, err := parseChatID(msg.ChatID)
	if err != nil {
		logger.ErrorCF("telegram", "Invalid chat ID in stream event", map[string]interface{}{
			"chat_id": msg.ChatID,
		})
		return
	}

	stateInterface, _ := c.streamStates.LoadOrStore(msg.ChatID, NewStreamState())
	state := stateInterface.(*StreamState)

	// 1:1 from opencode processor.ts
	switch msg.Type {
	// Text events
	case bus.StreamEventTextStart:
		part := &Part{
			ID:   msg.PartID,
			Type: "text",
			Text: "",
			Time: struct{ Start, End int64 }{Start: time.Now().UnixMilli()},
		}
		state.AddPart(part)
		state.SetCurrentText(part)
		state.SetActive(true)

	case bus.StreamEventTextDelta:
		state.UpdatePartDelta(msg.PartID, msg.Delta)
		// Throttle updates to avoid rate limiting
		if time.Since(state.lastUpdate) > 500*time.Millisecond {
			c.updateStreamMessage(ctx, chatID, state)
		}

	case bus.StreamEventTextEnd:
		if part := state.GetPart(msg.PartID); part != nil {
			part.Text = strings.TrimRight(part.Text, " \n\t")
			part.Time.End = time.Now().UnixMilli()
		}
		state.SetCurrentText(nil)
		c.updateStreamMessage(ctx, chatID, state)

	// Reasoning events
	case bus.StreamEventReasoningStart:
		part := &Part{
			ID:   msg.ID,
			Type: "reasoning",
			Text: "",
			Time: struct{ Start, End int64 }{Start: time.Now().UnixMilli()},
		}
		state.AddPart(part)
		state.AddReasoning(msg.ID, part)

	case bus.StreamEventReasoningDelta:
		state.UpdateReasoningDelta(msg.ID, msg.Delta)
		if time.Since(state.lastUpdate) > 500*time.Millisecond {
			c.updateStreamMessage(ctx, chatID, state)
		}

	case bus.StreamEventReasoningEnd:
		if part := state.GetReasoning(msg.ID); part != nil {
			part.Text = strings.TrimRight(part.Text, " \n\t")
			part.Time.End = time.Now().UnixMilli()
		}
		state.RemoveReasoning(msg.ID)
		c.updateStreamMessage(ctx, chatID, state)

	// Tool events
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

	case bus.StreamEventToolInputDelta:
		// Tool input deltas are typically JSON fragments
		if part := state.GetToolCall(msg.ToolCallID); part != nil {
			if input, ok := part.State["input"].(map[string]interface{}); ok {
				// Accumulate input
				_ = input
			}
		}

	case bus.StreamEventToolInputEnd:
		if part := state.GetToolCall(msg.ToolCallID); part != nil {
			part.State["status"] = "running"
		}

	case bus.StreamEventToolCall:
		if part := state.GetToolCall(msg.ToolCallID); part != nil {
			part.ToolName = msg.ToolName
			part.State["status"] = "running"
			part.State["input"] = msg.ToolArgs
			part.Time.Start = time.Now().UnixMilli()
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
			part.Time.End = time.Now().UnixMilli()
			// Don't remove from toolCalls - keep for display
			// state.RemoveToolCall(msg.ToolCallID)
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
			part.Time.End = time.Now().UnixMilli()
			// Don't remove from toolCalls - keep for display
			// state.RemoveToolCall(msg.ToolCallID)
		}
		c.updateStreamMessage(ctx, chatID, state)

	// Step events
	case bus.StreamEventStartStep:
		state.iteration = msg.Iteration
		state.AddExecutionStep(bus.ExecutionStep{
			Type:      "step_start",
			Timestamp: time.Now(),
			Iteration: msg.Iteration,
		})

	case bus.StreamEventFinishStep:
		state.AddExecutionStep(bus.ExecutionStep{
			Type:       "step_finish",
			Timestamp:  time.Now(),
			Iteration:  msg.Iteration,
			ToolResult: msg.FinishReason,
		})

	// Lifecycle events
	case bus.StreamEventStart:
		state.SetActive(true)

	case bus.StreamEventFinish:
		state.SetActive(false)
		c.finalizeStreamMessage(ctx, chatID, state, msg.SessionKey)
		c.streamStates.Delete(msg.ChatID)

	case bus.StreamEventError:
		state.SetActive(false)
		c.sendErrorMessage(ctx, chatID, msg.Content)
		c.streamStates.Delete(msg.ChatID)

	// Legacy events (向后兼容)
	case bus.StreamEventThinking:
		state.AddExecutionStep(bus.ExecutionStep{
			Type:      "thinking",
			Timestamp: time.Now(),
			Content:   msg.Content,
			Iteration: msg.Iteration,
		})
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventContent:
		// Legacy content event - treat as text-delta
		if state.currentText != nil {
			state.UpdatePartDelta(state.currentText.ID, msg.Content)
			if time.Since(state.lastUpdate) > 500*time.Millisecond {
				c.updateStreamMessage(ctx, chatID, state)
			}
		}

	case bus.StreamEventComplete:
		state.SetActive(false)
		if msg.Content != "" {
			part := &Part{
				ID:   "legacy-final",
				Type: "text",
				Text: msg.Content,
			}
			state.AddPart(part)
			state.SetCurrentText(part)
		}
		c.finalizeStreamMessage(ctx, chatID, state, msg.SessionKey)
		c.streamStates.Delete(msg.ChatID)
	}
}

func (c *TelegramChannel) updateStreamMessage(ctx context.Context, chatID int64, state *StreamState) {
	content := state.GetDisplayContent()
	if content == "" {
		return
	}

	htmlContent := markdownToTelegramHTML(content)

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
		logger.ErrorCF("telegram", "HTML parse failed, falling back to plain text", map[string]interface{}{
			"error": err.Error(),
		})
		msg.ParseMode = ""
		sentMsg, err = c.bot.SendMessage(ctx, msg)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to send stream message", map[string]interface{}{
				"error": err.Error(),
			})
			return
		}
	}

	state.SetMessageID(sentMsg.MessageID)
	state.SetChatID(chatID)
}

func (c *TelegramChannel) sendErrorMessage(ctx context.Context, chatID int64, errorMsg string) {
	htmlContent := markdownToTelegramHTML(fmt.Sprintf("❌ Error: %s", errorMsg))
	msg := tu.Message(tu.ID(chatID), htmlContent)
	msg.ParseMode = telego.ModeHTML

	if _, err := c.bot.SendMessage(ctx, msg); err != nil {
		msg.ParseMode = ""
		c.bot.SendMessage(ctx, msg)
	}
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
		logger.DebugCF("telegram", "Message rejected by allowlist", map[string]interface{}{
			"user_id":  userID,
			"username": user.Username,
		})
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
					logger.ErrorCF("telegram", "Voice transcription failed", map[string]interface{}{
						"error": err.Error(),
						"path":  voicePath,
					})
					transcribedText = "[voice (transcription failed)]"
				} else {
					transcribedText = fmt.Sprintf("[voice transcription: %s]", result.Text)
					logger.InfoCF("telegram", "Voice transcribed successfully", map[string]interface{}{
						"text": result.Text,
					})
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

	logger.DebugCF("telegram", "Received message", map[string]interface{}{
		"sender_id": senderID,
		"chat_id":   fmt.Sprintf("%d", chatID),
		"preview":   utils.Truncate(content, 50),
	})

	// Send typing action
	c.bot.SendChatAction(ctx, tu.ChatAction(tu.ID(chatID), telego.ChatActionTyping))

	// Send initial placeholder with opencode-style status
	placeholderMsg, err := c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), "⏳ 正在处理..."))
	if err == nil {
		stateInterface, _ := c.streamStates.LoadOrStore(fmt.Sprintf("%d", chatID), NewStreamState())
		state := stateInterface.(*StreamState)
		state.SetMessageID(placeholderMsg.MessageID)
		state.SetChatID(chatID)
	}

	metadata := map[string]string{
		"message_id": fmt.Sprintf("%d", message.MessageID),
		"user_id":    fmt.Sprintf("%d", user.ID),
		"username":   user.Username,
		"first_name": user.FirstName,
		"is_group":   fmt.Sprintf("%t", message.Chat.Type != "private"),
	}

	c.handleMessageWithStream(senderID, fmt.Sprintf("%d", chatID), content, mediaPaths, metadata)
}

func (c *TelegramChannel) handleMessageWithStream(senderID, chatID, content string, media []string, metadata map[string]string) {
	if !c.IsAllowed(senderID) {
		return
	}

	sessionKey := fmt.Sprintf("%s:%s", c.Name(), chatID)

	msg := bus.InboundMessage{
		Channel:    c.Name(),
		SenderID:   senderID,
		ChatID:     chatID,
		Content:    content,
		Media:      media,
		SessionKey: sessionKey,
		Metadata:   metadata,
		StreamMode: c.config.StreamMode,
	}

	c.bus.PublishInbound(msg)
}

func (c *TelegramChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return fmt.Errorf("telegram bot not running")
	}

	chatID, err := parseChatID(msg.ChatID)
	if err != nil {
		return fmt.Errorf("invalid chat ID: %w", err)
	}

	htmlContent := markdownToTelegramHTML(msg.Content)

	tgMsg := tu.Message(tu.ID(chatID), htmlContent)
	tgMsg.ParseMode = telego.ModeHTML

	if _, err = c.bot.SendMessage(ctx, tgMsg); err != nil {
		logger.ErrorCF("telegram", "HTML parse failed, falling back to plain text", map[string]interface{}{
			"error": err.Error(),
		})
		tgMsg.ParseMode = ""
		_, err = c.bot.SendMessage(ctx, tgMsg)
		return err
	}

	return nil
}

func (c *TelegramChannel) downloadPhoto(ctx context.Context, fileID string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get photo file", map[string]interface{}{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ".jpg")
}

func (c *TelegramChannel) downloadFileWithInfo(file *telego.File, ext string) string {
	if file.FilePath == "" {
		return ""
	}

	url := c.bot.FileDownloadURL(file.FilePath)
	logger.DebugCF("telegram", "File URL", map[string]interface{}{"url": url})

	filename := file.FilePath + ext
	return utils.DownloadFile(url, filename, utils.DownloadOptions{
		LoggerPrefix: "telegram",
	})
}

func (c *TelegramChannel) downloadFile(ctx context.Context, fileID, ext string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get file", map[string]interface{}{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ext)
}

func parseChatID(chatIDStr string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(chatIDStr, "%d", &id)
	return id, err
}

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	text = regexp.MustCompile(`^#{1,6}\s+(.+)$`).ReplaceAllString(text, "$1")

	text = regexp.MustCompile(`^>\s*(.*)$`).ReplaceAllString(text, "$1")

	text = escapeHTML(text)

	text = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`).ReplaceAllString(text, `<a href="$2">$1</a>`)

	text = regexp.MustCompile(`\*\*(.+?)\*\*`).ReplaceAllString(text, "<b>$1</b>")

	text = regexp.MustCompile(`__(.+?)__`).ReplaceAllString(text, "<b>$1</b>")

	reItalic := regexp.MustCompile(`_([^_]+)_`)
	text = reItalic.ReplaceAllStringFunc(text, func(s string) string {
		match := reItalic.FindStringSubmatch(s)
		if len(match) < 2 {
			return s
		}
		return "<i>" + match[1] + "</i>"
	})

	text = regexp.MustCompile(`~~(.+?)~~`).ReplaceAllString(text, "<s>$1</s>")

	text = regexp.MustCompile(`^[-*]\s+`).ReplaceAllString(text, "* ")

	for i, code := range inlineCodes.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00IC%d\x00", i), fmt.Sprintf("<code>%s</code>", escaped))
	}

	for i, code := range codeBlocks.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00CB%d\x00", i), fmt.Sprintf("<pre><code>%s</code></pre>", escaped))
	}

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

// ExecutionContext 存储完整的执行上下文，包括日志和最终回复
type ExecutionContext struct {
	Log          *bus.ExecutionLog
	FinalContent string
	ChatID       int64
	MessageID    int
}

// Execution log storage
type executionLogStore struct {
	sync.RWMutex
	logs map[string]*ExecutionContext
}

var globalLogStore = &executionLogStore{
	logs: make(map[string]*ExecutionContext),
}

func (c *TelegramChannel) saveExecutionLog(sessionKey string, log *bus.ExecutionLog, finalContent string, chatID int64, messageID int) {
	globalLogStore.Lock()
	defer globalLogStore.Unlock()
	globalLogStore.logs[sessionKey] = &ExecutionContext{
		Log:          log,
		FinalContent: finalContent,
		ChatID:       chatID,
		MessageID:    messageID,
	}
}

func (c *TelegramChannel) getExecutionContext(sessionKey string) *ExecutionContext {
	globalLogStore.RLock()
	defer globalLogStore.RUnlock()
	return globalLogStore.logs[sessionKey]
}

func (c *TelegramChannel) deleteExecutionLog(sessionKey string) {
	globalLogStore.Lock()
	defer globalLogStore.Unlock()
	delete(globalLogStore.logs, sessionKey)
}

func (c *TelegramChannel) finalizeStreamMessage(ctx context.Context, chatID int64, state *StreamState, sessionKey string) {
	content := state.GetDisplayContent()
	if content == "" {
		return
	}

	htmlContent := markdownToTelegramHTML(content)

	execLog := state.GetExecutionLog()
	var keyboard *telego.InlineKeyboardMarkup

	// Set EndTime for the execution log
	if execLog != nil {
		execLog.EndTime = time.Now()
	}

	if execLog != nil && len(execLog.Steps) > 0 {
		keyboard = tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				tu.InlineKeyboardButton("📋 View Details").WithCallbackData(fmt.Sprintf("view_log:%s", sessionKey)),
			),
		)
	}

	messageID := state.GetMessageID()
	finalMessageID := messageID

	if messageID != 0 {
		editMsg := tu.EditMessageText(tu.ID(chatID), messageID, htmlContent)
		editMsg.ParseMode = telego.ModeHTML
		if keyboard != nil {
			editMsg.ReplyMarkup = keyboard
		}
		if _, err := c.bot.EditMessageText(ctx, editMsg); err != nil {
			// Delete old message and send new one
			c.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
				ChatID:    telego.ChatID{ID: chatID},
				MessageID: messageID,
			})
			sentMsg := c.sendFinalMessageWithKeyboard(ctx, chatID, htmlContent, keyboard)
			if sentMsg != nil {
				finalMessageID = sentMsg.MessageID
			}
		}
	} else {
		sentMsg := c.sendFinalMessageWithKeyboard(ctx, chatID, htmlContent, keyboard)
		if sentMsg != nil {
			finalMessageID = sentMsg.MessageID
		}
	}

	// Save execution context after finalizing message
	if execLog != nil && len(execLog.Steps) > 0 {
		c.saveExecutionLog(sessionKey, execLog, content, chatID, finalMessageID)
	}
}

func (c *TelegramChannel) sendFinalMessageWithKeyboard(ctx context.Context, chatID int64, htmlContent string, keyboard *telego.InlineKeyboardMarkup) *telego.Message {
	msg := tu.Message(tu.ID(chatID), htmlContent)
	msg.ParseMode = telego.ModeHTML
	if keyboard != nil {
		msg.ReplyMarkup = keyboard
	}

	sentMsg, err := c.bot.SendMessage(ctx, msg)
	if err != nil {
		logger.ErrorCF("telegram", "Failed to send final message", map[string]interface{}{
			"error": err.Error(),
		})
		return nil
	}
	return sentMsg
}

func (c *TelegramChannel) handleCallbackQuery(ctx context.Context, update telego.Update) {
	if update.CallbackQuery == nil {
		return
	}

	callback := update.CallbackQuery
	data := callback.Data

	if strings.HasPrefix(data, "view_log:") {
		sessionKey := strings.TrimPrefix(data, "view_log:")
		execCtx := c.getExecutionContext(sessionKey)
		if execCtx == nil || execCtx.Log == nil {
			c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
				CallbackQueryID: callback.ID,
				Text:            "Log expired or not found",
				ShowAlert:       true,
			})
			return
		}

		detailContent := formatExecutionLog(execCtx.Log)
		// Use plain text mode (no HTML parsing) to preserve formatting
		// Escape HTML characters to prevent parsing errors
		escapedContent := escapeHTMLForTelegram(detailContent)

		_, err := c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: callback.Message.GetChat().ID},
			MessageID: callback.Message.GetMessageID(),
			Text:      escapedContent,
			ParseMode: "", // Plain text mode to preserve newlines
			ReplyMarkup: tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					tu.InlineKeyboardButton("◀️ Back").WithCallbackData(fmt.Sprintf("back_result:%s", sessionKey)),
				),
			),
		})

		if err != nil {
			logger.ErrorCF("telegram", "Failed to edit message for view_log", map[string]interface{}{
				"error":       err.Error(),
				"session_key": sessionKey,
			})
		}

		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
		})
	} else if strings.HasPrefix(data, "back_result:") {
		sessionKey := strings.TrimPrefix(data, "back_result:")
		execCtx := c.getExecutionContext(sessionKey)
		if execCtx == nil {
			c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
				CallbackQueryID: callback.ID,
				Text:            "Log expired",
				ShowAlert:       true,
			})
			return
		}

		// Use the stored final content instead of trying to extract from steps
		finalContent := execCtx.FinalContent
		if finalContent == "" {
			finalContent = "✅ Completed"
		}

		htmlContent := markdownToTelegramHTML(finalContent)
		_, err := c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: callback.Message.GetChat().ID},
			MessageID: callback.Message.GetMessageID(),
			Text:      htmlContent,
			ParseMode: telego.ModeHTML,
			ReplyMarkup: tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					tu.InlineKeyboardButton("📋 View Details").WithCallbackData(fmt.Sprintf("view_log:%s", sessionKey)),
				),
			),
		})

		if err != nil {
			logger.ErrorCF("telegram", "Failed to edit message for back_result", map[string]interface{}{
				"error":       err.Error(),
				"session_key": sessionKey,
			})
		}

		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
		})
	}
}

// escapeHTMLForTelegram escapes HTML characters for Telegram plain text mode
func escapeHTMLForTelegram(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

// formatExecutionLog formats execution log for Telegram display
// Uses plain text mode (not HTML) to preserve formatting
func formatExecutionLog(log *bus.ExecutionLog) string {
	var sb strings.Builder

	sb.WriteString("📋 Execution Log\n")
	sb.WriteString(fmt.Sprintf("🕐 Start: %s\n", log.StartTime.Format("15:04:05")))
	if !log.EndTime.IsZero() {
		duration := log.EndTime.Sub(log.StartTime)
		sb.WriteString(fmt.Sprintf("⏱️ Duration: %s\n", duration.Round(time.Millisecond)))
	}
	sb.WriteString("\n")

	sb.WriteString("─" + strings.Repeat("─", 30) + "\n\n")

	for i, step := range log.Steps {
		switch step.Type {
		case "thinking":
			sb.WriteString(fmt.Sprintf("%d. 💭 Thinking (Step %d)\n", i+1, step.Iteration))
			if step.Content != "" {
				sb.WriteString(fmt.Sprintf("   %s\n", step.Content))
			}
		case "tool_call":
			sb.WriteString(fmt.Sprintf("%d. 🔧 Tool Call (Step %d)\n", i+1, step.Iteration))
			sb.WriteString(fmt.Sprintf("   Tool: %s\n", step.ToolName))
			if len(step.ToolArgs) > 0 {
				argsJSON, _ := json.Marshal(step.ToolArgs)
				argsStr := string(argsJSON)
				if len(argsStr) > 300 {
					argsStr = argsStr[:297] + "..."
				}
				sb.WriteString(fmt.Sprintf("   Args: %s\n", argsStr))
			}
		case "tool_result":
			sb.WriteString(fmt.Sprintf("%d. ✅ Tool Result\n", i+1))
			if step.ToolName != "" {
				sb.WriteString(fmt.Sprintf("   Tool: %s\n", step.ToolName))
			}
			if step.ToolResult != "" {
				preview := step.ToolResult
				if len(preview) > 800 {
					preview = preview[:797] + "..."
				}
				sb.WriteString(fmt.Sprintf("   Result: %s\n", preview))
			}
		case "step_start":
			sb.WriteString(fmt.Sprintf("%d. ▶️ Step %d Started\n", i+1, step.Iteration))
		case "step_finish":
			sb.WriteString(fmt.Sprintf("%d. ⏹️ Step Finished\n", i+1))
			if step.ToolResult != "" {
				sb.WriteString(fmt.Sprintf("   Reason: %s\n", step.ToolResult))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
