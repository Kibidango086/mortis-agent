// Package telegram implements Telegram channel with opencode-style streaming
package telegram

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
	"github.com/Kibidango086/mortis-agent/pkg/message"
	"github.com/Kibidango086/mortis-agent/pkg/stream"
	"github.com/Kibidango086/mortis-agent/pkg/utils"
	"github.com/Kibidango086/mortis-agent/pkg/voice"
)

// StreamSession manages a single streaming session
type StreamSession struct {
	SessionID    string
	ChatID       int64
	MessageID    int
	Processor    *stream.Processor
	FinalText    string
	HasToolCalls bool
	mu           sync.RWMutex
}

func NewStreamSession(sessionID string, chatID int64) *StreamSession {
	return &StreamSession{
		SessionID: sessionID,
		ChatID:    chatID,
		Processor: stream.NewProcessor(sessionID, "assistant-msg"),
	}
}

func (s *StreamSession) SetMessageID(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MessageID = id
}

func (s *StreamSession) GetMessageID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.MessageID
}

func (s *StreamSession) ProcessEvent(event stream.Event) error {
	part, err := s.Processor.ProcessEvent(event)
	if err != nil {
		return err
	}

	// Track if we have tool calls
	if part != nil {
		if _, ok := part.(*message.ToolPart); ok {
			s.HasToolCalls = true
		}
	}

	return nil
}

func (s *StreamSession) GetDisplayContent() string {
	var parts []string

	// Show tool calls - last 3
	toolCalls := s.Processor.GetToolCalls()
	if len(toolCalls) > 0 {
		start := 0
		if len(toolCalls) > 3 {
			start = len(toolCalls) - 3
		}

		for i := start; i < len(toolCalls); i++ {
			tool := toolCalls[i]
			var toolBlock strings.Builder
			toolBlock.WriteString(fmt.Sprintf("🔧 %s\n", tool.ToolName))

			if len(tool.ToolArgs) > 0 {
				argsJSON, _ := json.MarshalIndent(tool.ToolArgs, "", "  ")
				toolBlock.WriteString("```\nInput:\n")
				toolBlock.WriteString(string(argsJSON))
				toolBlock.WriteString("\n```")
			}

			if tool.ToolResult != "" {
				toolBlock.WriteString("```\nOutput:\n")
				toolBlock.WriteString(tool.ToolResult)
				toolBlock.WriteString("\n```")
			}

			parts = append(parts, toolBlock.String())
		}

		if len(toolCalls) > 3 {
			parts = append(parts, fmt.Sprintf("📋 ... and %d more", len(toolCalls)-3))
		}
	}

	// Show final text
	finalText := s.Processor.GetFinalText()
	if finalText != "" {
		if len(parts) > 0 {
			parts = append(parts, "")
		}
		parts = append(parts, finalText)
	}

	return strings.Join(parts, "\n")
}

func (s *StreamSession) GetFinalText() string {
	return s.Processor.GetFinalText()
}

// TelegramChannel implements Telegram integration
type TelegramChannel struct {
	bot         *telego.Bot
	config      config.TelegramConfig
	bus         *bus.MessageBus
	chatIDs     map[string]int64
	transcriber *voice.GroqTranscriber
	sessions    sync.Map // sessionID -> *StreamSession
	mu          sync.RWMutex
}

// NewTelegramChannel creates a new Telegram channel
func NewTelegramChannel(cfg config.TelegramConfig, msgBus *bus.MessageBus) (*TelegramChannel, error) {
	var opts []telego.BotOption

	if cfg.Proxy != "" {
		proxyURL, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL: %w", err)
		}
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}))
	}

	bot, err := telego.NewBot(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	return &TelegramChannel{
		bot:     bot,
		config:  cfg,
		bus:     msgBus,
		chatIDs: make(map[string]int64),
	}, nil
}

func (c *TelegramChannel) SetTranscriber(t *voice.GroqTranscriber) {
	c.transcriber = t
}

func (c *TelegramChannel) Start(ctx context.Context) error {
	logger.InfoC("telegram", "Starting Telegram bot...")

	updates, err := c.bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{Timeout: 30})
	if err != nil {
		return fmt.Errorf("failed to start polling: %w", err)
	}

	logger.InfoCF("telegram", "Bot connected", map[string]interface{}{"username": c.bot.Username()})

	// Start stream handler
	go c.handleStreamEvents(ctx)

	// Start message handler
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updates:
				if !ok {
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
	return nil
}

func (c *TelegramChannel) handleStreamEvents(ctx context.Context) {
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

			c.handleStreamMessage(ctx, msg)
		}
	}
}

func (c *TelegramChannel) handleStreamMessage(ctx context.Context, msg bus.StreamMessage) {
	if msg.SessionKey == "heartbeat" {
		return
	}

	chatID, err := parseChatID(msg.ChatID)
	if err != nil {
		logger.ErrorCF("telegram", "Invalid chat ID", map[string]interface{}{"chat_id": msg.ChatID})
		return
	}

	// Get or create session
	sessionInterface, _ := c.sessions.LoadOrStore(msg.SessionKey, NewStreamSession(msg.SessionKey, chatID))
	session := sessionInterface.(*StreamSession)

	// Convert bus event to stream event
	event := stream.Event{
		Type:             stream.EventType(msg.Type),
		ID:               msg.ID,
		Delta:            msg.Delta,
		PartID:           msg.PartID,
		ToolCallID:       msg.ToolCallID,
		ToolName:         msg.ToolName,
		ToolInput:        msg.ToolArgs,
		ToolResult:       msg.ToolResult,
		Error:            fmt.Errorf("%s", msg.Error),
		FinishReason:     msg.FinishReason,
		ProviderMetadata: map[string]interface{}{},
	}

	// Process the event
	if err := session.ProcessEvent(event); err != nil {
		logger.ErrorCF("telegram", "Failed to process event", map[string]interface{}{
			"error": err.Error(),
			"type":  msg.Type,
		})
		return
	}

	// Update display
	switch msg.Type {
	case bus.StreamEventTextDelta, bus.StreamEventToolResult, bus.StreamEventToolCall:
		// Throttle updates
		c.updateDisplay(ctx, session)
	case bus.StreamEventFinish:
		// Final update
		c.finalizeSession(ctx, session, msg.SessionKey)
		c.sessions.Delete(msg.SessionKey)
	}
}

func (c *TelegramChannel) updateDisplay(ctx context.Context, session *StreamSession) {
	content := session.GetDisplayContent()
	if content == "" {
		return
	}

	htmlContent := markdownToTelegramHTML(content)

	// Truncate if too long
	if len(htmlContent) > 4000 {
		htmlContent = htmlContent[:4000] + "\n\n<i>[...]</i>"
	}

	messageID := session.GetMessageID()
	if messageID != 0 {
		editMsg := tu.EditMessageText(tu.ID(session.ChatID), messageID, htmlContent)
		editMsg.ParseMode = telego.ModeHTML
		if _, err := c.bot.EditMessageText(ctx, editMsg); err != nil {
			// Edit failed, send new message
			c.sendNewMessage(ctx, session, htmlContent)
		}
	} else {
		c.sendNewMessage(ctx, session, htmlContent)
	}
}

func (c *TelegramChannel) sendNewMessage(ctx context.Context, session *StreamSession, htmlContent string) {
	msg := tu.Message(tu.ID(session.ChatID), htmlContent)
	msg.ParseMode = telego.ModeHTML

	sentMsg, err := c.bot.SendMessage(ctx, msg)
	if err != nil {
		// Try without HTML
		msg.ParseMode = ""
		sentMsg, err = c.bot.SendMessage(ctx, msg)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to send message", map[string]interface{}{"error": err.Error()})
			return
		}
	}

	session.SetMessageID(sentMsg.MessageID)
}

func (c *TelegramChannel) finalizeSession(ctx context.Context, session *StreamSession, sessionKey string) {
	finalText := session.GetFinalText()

	// Store execution log for View Details
	if session.HasToolCalls {
		executionLogStore.Store(sessionKey, &ExecutionContext{
			Steps:        session.Processor.GetToolCalls(),
			FinalContent: finalText,
			ChatID:       session.ChatID,
			MessageID:    session.GetMessageID(),
		})
	}
}

func (c *TelegramChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if msg.SessionID == "heartbeat" {
		return nil
	}

	chatID, err := parseChatID(msg.ChatID)
	if err != nil {
		return fmt.Errorf("invalid chat ID: %w", err)
	}

	// Get execution context for View Details button
	execCtx, exists := c.getExecutionContext(msg.SessionID)

	finalContent := markdownToTelegramHTML(msg.Content)
	if len(finalContent) > 4000 {
		finalContent = finalContent[:4000] + "\n\n<i>[...]</i>"
	}

	if exists && execCtx.MessageID != 0 {
		// Edit existing message
		editMsg := tu.EditMessageText(tu.ID(chatID), execCtx.MessageID, finalContent)
		editMsg.ParseMode = telego.ModeHTML

		if execCtx.HasToolCalls() {
			editMsg.ReplyMarkup = tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					tu.InlineKeyboardButton("📋 View Details").WithCallbackData(fmt.Sprintf("view_log:%s", msg.SessionID)),
				),
			)
		}

		if _, err := c.bot.EditMessageText(ctx, editMsg); err == nil {
			c.deleteExecutionContext(msg.SessionID)
			return nil
		}
	}

	// Send new message
	tgMsg := tu.Message(tu.ID(chatID), finalContent)
	tgMsg.ParseMode = telego.ModeHTML

	if exists && execCtx.HasToolCalls() {
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

	c.deleteExecutionContext(msg.SessionID)
	return nil
}

// ExecutionContext stores execution context for View Details
type ExecutionContext struct {
	Steps        []message.ExecutionStep
	FinalContent string
	ChatID       int64
	MessageID    int
}

func (e *ExecutionContext) HasToolCalls() bool {
	return len(e.Steps) > 0
}

var executionLogStore sync.Map

func (c *TelegramChannel) getExecutionContext(sessionID string) (*ExecutionContext, bool) {
	if val, ok := executionLogStore.Load(sessionID); ok {
		return val.(*ExecutionContext), true
	}
	return nil, false
}

func (c *TelegramChannel) deleteExecutionContext(sessionID string) {
	executionLogStore.Delete(sessionID)
}

func (c *TelegramChannel) handleCallbackQuery(ctx context.Context, update telego.Update) {
	if update.CallbackQuery == nil {
		return
	}

	callback := update.CallbackQuery
	data := callback.Data

	if strings.HasPrefix(data, "view_log:") {
		sessionKey := strings.TrimPrefix(data, "view_log:")
		execCtx, exists := c.getExecutionContext(sessionKey)
		if !exists {
			c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
				CallbackQueryID: callback.ID,
				Text:            "Details not available",
				ShowAlert:       true,
			})
			return
		}
		c.showToolCallsPage(ctx, callback, sessionKey, execCtx, 0)
		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: callback.ID})
	} else if strings.HasPrefix(data, "tools_page:") {
		parts := strings.Split(data, ":")
		if len(parts) < 3 {
			return
		}
		sessionKey := parts[1]
		pageNum := 0
		fmt.Sscanf(parts[2], "%d", &pageNum)

		execCtx, exists := c.getExecutionContext(sessionKey)
		if !exists {
			return
		}

		c.showToolCallsPage(ctx, callback, sessionKey, execCtx, pageNum)
		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: callback.ID})
	} else if strings.HasPrefix(data, "back_result:") {
		sessionKey := strings.TrimPrefix(data, "back_result:")
		execCtx, exists := c.getExecutionContext(sessionKey)
		if !exists {
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

		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: callback.ID})
	}
}

func (c *TelegramChannel) showToolCallsPage(ctx context.Context, callback *telego.CallbackQuery, sessionKey string, execCtx *ExecutionContext, pageNum int) {
	if len(execCtx.Steps) == 0 {
		return
	}

	toolsPerPage := 3
	totalPages := (len(execCtx.Steps) + toolsPerPage - 1) / toolsPerPage

	if pageNum < 0 {
		pageNum = 0
	}
	if pageNum >= totalPages {
		pageNum = totalPages - 1
	}

	startIdx := pageNum * toolsPerPage
	endIdx := startIdx + toolsPerPage
	if endIdx > len(execCtx.Steps) {
		endIdx = len(execCtx.Steps)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 Tool Calls (Page %d/%d)\n\n", pageNum+1, totalPages))
	sb.WriteString(strings.Repeat("─", 32))
	sb.WriteString("\n\n")

	pageTools := execCtx.Steps[startIdx:endIdx]
	for i, tool := range pageTools {
		displayNum := startIdx + i + 1
		sb.WriteString(fmt.Sprintf("%d. 🔧 %s\n", displayNum, tool.ToolName))

		if len(tool.ToolArgs) > 0 {
			argsJSON, _ := json.MarshalIndent(tool.ToolArgs, "", "  ")
			argsStr := string(argsJSON)
			if len(argsStr) > 500 {
				argsStr = argsStr[:497] + "..."
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
			if len(result) > 500 {
				result = result[:497] + "..."
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
	escapedContent := escapeHTML(content)

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

	// Check allowlist
	if !c.isAllowed(senderID) {
		return
	}

	chatID := message.Chat.ID
	c.mu.Lock()
	c.chatIDs[senderID] = chatID
	c.mu.Unlock()

	// Extract content
	content := c.extractContent(ctx, message)

	// Initialize stream session
	sessionKey := fmt.Sprintf("telegram:%d", chatID)
	session := NewStreamSession(sessionKey, chatID)
	c.sessions.Store(sessionKey, session)

	// Send typing action
	c.bot.SendChatAction(ctx, tu.ChatAction(tu.ID(chatID), telego.ChatActionTyping))

	// Publish inbound message
	msg := bus.InboundMessage{
		Channel:    "telegram",
		SenderID:   senderID,
		ChatID:     fmt.Sprintf("%d", chatID),
		Content:    content,
		SessionKey: sessionKey,
		StreamMode: true,
	}

	c.bus.PublishInbound(msg)
}

func (c *TelegramChannel) extractContent(ctx context.Context, message *telego.Message) string {
	var content string

	if message.Text != "" {
		content = message.Text
	}

	if message.Caption != "" {
		if content != "" {
			content += "\n"
		}
		content += message.Caption
	}

	if message.Photo != nil && len(message.Photo) > 0 {
		photo := message.Photo[len(message.Photo)-1]
		if path := c.downloadPhoto(ctx, photo.FileID); path != "" {
			if content != "" {
				content += "\n"
			}
			content += "[image: photo]"
		}
	}

	if message.Voice != nil {
		if path := c.downloadFile(ctx, message.Voice.FileID, ".ogg"); path != "" {
			transcribed := c.transcribeVoice(ctx, path)
			if content != "" {
				content += "\n"
			}
			content += transcribed
		}
	}

	if content == "" {
		content = "[empty message]"
	}

	return content
}

func (c *TelegramChannel) isAllowed(senderID string) bool {
	// TODO: Implement allowlist check
	return true
}

func (c *TelegramChannel) transcribeVoice(ctx context.Context, path string) string {
	if c.transcriber == nil || !c.transcriber.IsAvailable() {
		return "[voice]"
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := c.transcriber.Transcribe(ctx, path)
	if err != nil {
		return "[voice (transcription failed)]"
	}

	return fmt.Sprintf("[voice transcription: %s]", result.Text)
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

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	// Extract code blocks
	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	// Convert markdown
	text = regexp.MustCompile(`^#{1,6}\s+(.+)$`).ReplaceAllString(text, "<b>$1</b>")
	text = regexp.MustCompile(`^>\s*(.*)$`).ReplaceAllString(text, "<i>$1</i>")
	text = escapeHTML(text)
	text = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`).ReplaceAllString(text, `<a href="$2">$1</a>`)
	text = regexp.MustCompile(`\*\*(.+?)\*\*`).ReplaceAllString(text, "<b>$1</b>")
	text = regexp.MustCompile(`__(.+?)__`).ReplaceAllString(text, "<b>$1</b>")
	text = regexp.MustCompile(`(?<!\*)\*([^\*]+)\*(?!\*)`).ReplaceAllString(text, "<i>$1</i>")
	text = regexp.MustCompile(`(?<!_)_([^_]+)_(?!_)`).ReplaceAllString(text, "<i>$1</i>")
	text = regexp.MustCompile(`~~(.+?)~~`).ReplaceAllString(text, "<s>$1</s>")
	text = regexp.MustCompile(`(?m)^[-*]\s+(.+)$`).ReplaceAllString(text, "• $1")

	// Restore codes
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
