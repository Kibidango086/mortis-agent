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

type StreamState struct {
	messageID      int
	currentContent string
	lastUpdate     time.Time
	isStreaming    bool
	stepInfo       string
	iteration      int
	toolCalls      []string
	executionLog   *bus.ExecutionLog
	mu             sync.RWMutex
}

func (s *StreamState) UpdateContent(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentContent = content
	s.lastUpdate = time.Now()
}

func (s *StreamState) AppendContent(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentContent += content
	s.lastUpdate = time.Now()
}

func (s *StreamState) SetStep(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stepInfo = step
}

func (s *StreamState) AddToolCall(toolName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls = append(s.toolCalls, toolName)
}

func (s *StreamState) GetDisplayContent() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var parts []string

	if s.stepInfo != "" {
		parts = append(parts, fmt.Sprintf("%s", s.stepInfo))
	}

	if len(s.toolCalls) > 0 {
		toolsStr := strings.Join(s.toolCalls, " -> ")
		parts = append(parts, fmt.Sprintf("Tool: %s", toolsStr))
	}

	if s.iteration > 0 {
		parts = append(parts, fmt.Sprintf("Round %d", s.iteration))
	}

	if len(parts) > 0 && s.currentContent != "" {
		parts = append(parts, "\n"+strings.Repeat("-", 20)+"\n")
	}

	if s.currentContent != "" {
		parts = append(parts, s.currentContent)
	}

	return strings.Join(parts, "\n")
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

func (s *StreamState) GetContent() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentContent
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

type TelegramChannel struct {
	*BaseChannel
	bot          *telego.Bot
	config       config.TelegramConfig
	chatIDs      map[string]int64
	transcriber  *voice.GroqTranscriber
	placeholders sync.Map
	stopThinking sync.Map
	streamStates sync.Map
}

type thinkingCancel struct {
	fn context.CancelFunc
}

func (c *thinkingCancel) Cancel() {
	if c != nil && c.fn != nil {
		c.fn()
	}
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
		BaseChannel:  base,
		bot:          bot,
		config:       cfg,
		chatIDs:      make(map[string]int64),
		transcriber:  nil,
		placeholders: sync.Map{},
		stopThinking: sync.Map{},
		streamStates: sync.Map{},
	}, nil
}

func (c *TelegramChannel) SetTranscriber(transcriber *voice.GroqTranscriber) {
	c.transcriber = transcriber
}

func (c *TelegramChannel) Start(ctx context.Context) error {
	logger.InfoC("telegram", "Starting Telegram bot (streaming mode)...")

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
	chatID, err := parseChatID(msg.ChatID)
	if err != nil {
		logger.ErrorCF("telegram", "Invalid chat ID in stream event", map[string]interface{}{
			"chat_id": msg.ChatID,
		})
		return
	}

	stateInterface, _ := c.streamStates.LoadOrStore(msg.ChatID, &StreamState{})
	state := stateInterface.(*StreamState)

	switch msg.Type {
	case bus.StreamEventThinking:
		state.SetStep(msg.Content)
		state.SetActive(true)
		state.AddExecutionStep(bus.ExecutionStep{
			Type:      "thinking",
			Timestamp: msg.Timestamp,
			Content:   msg.Content,
			Iteration: msg.Iteration,
		})
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventToolCall:
		state.AddToolCall(msg.ToolName)
		state.iteration = msg.Iteration
		state.AddExecutionStep(bus.ExecutionStep{
			Type:      "tool_call",
			Timestamp: msg.Timestamp,
			ToolName:  msg.ToolName,
			ToolArgs:  msg.ToolArgs,
			Iteration: msg.Iteration,
		})
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventToolResult:
		state.SetStep("")
		state.AddExecutionStep(bus.ExecutionStep{
			Type:       "tool_result",
			Timestamp:  msg.Timestamp,
			ToolName:   msg.ToolName,
			ToolResult: msg.ToolResult,
			Iteration:  msg.Iteration,
		})

	case bus.StreamEventContent:
		state.AppendContent(msg.Content)
		state.iteration = msg.Iteration
		if time.Since(state.lastUpdate) > 500*time.Millisecond {
			c.updateStreamMessage(ctx, chatID, state)
		}

	case bus.StreamEventComplete:
		state.UpdateContent(msg.Content)
		state.SetActive(false)
		state.SetStep("")
		if state.executionLog != nil {
			state.executionLog.EndTime = time.Now()
		}
		c.finalizeStreamMessage(ctx, chatID, state, msg.SessionKey)
		c.streamStates.Delete(msg.ChatID)

	case bus.StreamEventError:
		state.UpdateContent(fmt.Sprintf("Error: %s", msg.Content))
		state.SetActive(false)
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

	if state.messageID != 0 {
		editMsg := tu.EditMessageText(tu.ID(chatID), state.messageID, htmlContent)
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

	state.messageID = sentMsg.MessageID
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
	c.chatIDs[senderID] = chatID

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
			content += fmt.Sprintf("[image: photo]")
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
					transcribedText = fmt.Sprintf("[voice (transcription failed)]")
				} else {
					transcribedText = fmt.Sprintf("[voice transcription: %s]", result.Text)
					logger.InfoCF("telegram", "Voice transcribed successfully", map[string]interface{}{
						"text": result.Text,
					})
				}
			} else {
				transcribedText = fmt.Sprintf("[voice]")
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
			content += fmt.Sprintf("[audio]")
		}
	}

	if message.Document != nil {
		docPath := c.downloadFile(ctx, message.Document.FileID, "")
		if docPath != "" {
			mediaPaths = append(mediaPaths, docPath)
			if content != "" {
				content += "\n"
			}
			content += fmt.Sprintf("[file]")
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

	err := c.bot.SendChatAction(ctx, tu.ChatAction(tu.ID(chatID), telego.ChatActionTyping))
	if err != nil {
		logger.ErrorCF("telegram", "Failed to send chat action", map[string]interface{}{
			"error": err.Error(),
		})
	}

	chatIDStr := fmt.Sprintf("%d", chatID)
	if prevStop, ok := c.stopThinking.Load(chatIDStr); ok {
		if cf, ok := prevStop.(*thinkingCancel); ok && cf != nil {
			cf.Cancel()
		}
	}

	_, thinkCancel := context.WithTimeout(ctx, 5*time.Minute)
	c.stopThinking.Store(chatIDStr, &thinkingCancel{fn: thinkCancel})

	pMsg, err := c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), "Thinking..."))
	if err == nil {
		c.placeholders.Store(chatIDStr, pMsg.MessageID)
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

	if stop, ok := c.stopThinking.Load(msg.ChatID); ok {
		if cf, ok := stop.(*thinkingCancel); ok && cf != nil {
			cf.Cancel()
		}
		c.stopThinking.Delete(msg.ChatID)
	}

	htmlContent := markdownToTelegramHTML(msg.Content)

	if pID, ok := c.placeholders.Load(msg.ChatID); ok {
		c.placeholders.Delete(msg.ChatID)
		editMsg := tu.EditMessageText(tu.ID(chatID), pID.(int), htmlContent)
		editMsg.ParseMode = telego.ModeHTML

		if _, err = c.bot.EditMessageText(ctx, editMsg); err == nil {
			return nil
		}
	}

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

type executionLogStore struct {
	sync.RWMutex
	logs map[string]*bus.ExecutionLog
}

var globalLogStore = &executionLogStore{
	logs: make(map[string]*bus.ExecutionLog),
}

func (c *TelegramChannel) saveExecutionLog(sessionKey string, log *bus.ExecutionLog) {
	globalLogStore.Lock()
	defer globalLogStore.Unlock()
	globalLogStore.logs[sessionKey] = log
}

func (c *TelegramChannel) getExecutionLog(sessionKey string) *bus.ExecutionLog {
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
	content := state.GetContent()
	if content == "" {
		return
	}

	htmlContent := markdownToTelegramHTML(content)

	execLog := state.GetExecutionLog()
	var keyboard *telego.InlineKeyboardMarkup
	if execLog != nil && len(execLog.Steps) > 0 {
		keyboard = tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				tu.InlineKeyboardButton("View Details").WithCallbackData(fmt.Sprintf("view_log:%s", sessionKey)),
			),
		)
		c.saveExecutionLog(sessionKey, execLog)
	}

	if state.messageID != 0 {
		editMsg := tu.EditMessageText(tu.ID(chatID), state.messageID, htmlContent)
		editMsg.ParseMode = telego.ModeHTML
		if keyboard != nil {
			editMsg.ReplyMarkup = keyboard
		}
		if _, err := c.bot.EditMessageText(ctx, editMsg); err != nil {
			c.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
				ChatID:    telego.ChatID{ID: chatID},
				MessageID: state.messageID,
			})
			c.sendFinalMessageWithKeyboard(ctx, chatID, htmlContent, keyboard)
		}
	} else {
		c.sendFinalMessageWithKeyboard(ctx, chatID, htmlContent, keyboard)
	}
}

func (c *TelegramChannel) sendFinalMessageWithKeyboard(ctx context.Context, chatID int64, htmlContent string, keyboard *telego.InlineKeyboardMarkup) {
	msg := tu.Message(tu.ID(chatID), htmlContent)
	msg.ParseMode = telego.ModeHTML
	if keyboard != nil {
		msg.ReplyMarkup = keyboard
	}

	if _, err := c.bot.SendMessage(ctx, msg); err != nil {
		logger.ErrorCF("telegram", "Failed to send final message", map[string]interface{}{
			"error": err.Error(),
		})
	}
}

func (c *TelegramChannel) handleCallbackQuery(ctx context.Context, update telego.Update) {
	if update.CallbackQuery == nil {
		return
	}

	callback := update.CallbackQuery
	data := callback.Data

	if strings.HasPrefix(data, "view_log:") {
		sessionKey := strings.TrimPrefix(data, "view_log:")
		log := c.getExecutionLog(sessionKey)
		if log == nil {
			c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
				CallbackQueryID: callback.ID,
				Text:            "Log expired or not found",
				ShowAlert:       true,
			})
			return
		}

		detailContent := formatExecutionLog(log)
		htmlContent := markdownToTelegramHTML(detailContent)

		c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: callback.Message.GetChat().ID},
			MessageID: callback.Message.GetMessageID(),
			Text:      htmlContent,
			ParseMode: telego.ModeHTML,
			ReplyMarkup: tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					tu.InlineKeyboardButton("Back").WithCallbackData(fmt.Sprintf("back_result:%s", sessionKey)),
				),
			),
		})

		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
		})
	} else if strings.HasPrefix(data, "back_result:") {
		sessionKey := strings.TrimPrefix(data, "back_result:")
		log := c.getExecutionLog(sessionKey)
		if log == nil {
			c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
				CallbackQueryID: callback.ID,
				Text:            "Log expired",
				ShowAlert:       true,
			})
			return
		}

		var finalContent string
		for i := len(log.Steps) - 1; i >= 0; i-- {
			if log.Steps[i].Type == "thinking" && log.Steps[i].Content != "" {
				finalContent = log.Steps[i].Content
				break
			}
		}
		if finalContent == "" {
			finalContent = "Completed"
		}

		htmlContent := markdownToTelegramHTML(finalContent)
		c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: callback.Message.GetChat().ID},
			MessageID: callback.Message.GetMessageID(),
			Text:      htmlContent,
			ParseMode: telego.ModeHTML,
			ReplyMarkup: tu.InlineKeyboard(
				tu.InlineKeyboardRow(
					tu.InlineKeyboardButton("View Details").WithCallbackData(fmt.Sprintf("view_log:%s", sessionKey)),
				),
			),
		})

		c.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
		})
	}
}

func formatExecutionLog(log *bus.ExecutionLog) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Execution Log\n"))
	sb.WriteString(fmt.Sprintf("Start: %s\n", log.StartTime.Format("15:04:05")))
	if !log.EndTime.IsZero() {
		duration := log.EndTime.Sub(log.StartTime)
		sb.WriteString(fmt.Sprintf("Duration: %s\n\n", duration.Round(time.Millisecond)))
	}

	sb.WriteString("---\n")
	for i, step := range log.Steps {
		switch step.Type {
		case "thinking":
			sb.WriteString(fmt.Sprintf("%d. Thinking (Round %d)\n", i+1, step.Iteration))
			if step.Content != "" {
				sb.WriteString(fmt.Sprintf("   %s\n", step.Content))
			}
		case "tool_call":
			sb.WriteString(fmt.Sprintf("%d. Tool Call (Round %d)\n", i+1, step.Iteration))
			sb.WriteString(fmt.Sprintf("   Tool: %s\n", step.ToolName))
			if len(step.ToolArgs) > 0 {
				argsJSON, _ := json.Marshal(step.ToolArgs)
				if len(argsJSON) > 200 {
					argsJSON = append(argsJSON[:200], []byte("...")...)
				}
				sb.WriteString(fmt.Sprintf("   Args: %s\n", string(argsJSON)))
			}
		case "tool_result":
			sb.WriteString(fmt.Sprintf("%d. Tool Result\n", i+1))
			if step.ToolName != "" {
				sb.WriteString(fmt.Sprintf("   Tool: %s\n", step.ToolName))
			}
			if step.ToolResult != "" {
				preview := step.ToolResult
				if len(preview) > 500 {
					preview = preview[:500] + "..."
				}
				sb.WriteString(fmt.Sprintf("   Result: %s\n", preview))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
