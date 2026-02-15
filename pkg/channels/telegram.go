package channels

import (
	"context"
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

// StreamState 管理单个聊天的流式状态
type StreamState struct {
	messageID      int
	currentContent string
	lastUpdate     time.Time
	isStreaming    bool
	stepInfo       string   // 当前步骤信息（思考中、调用工具等）
	iteration      int      // 当前迭代次数
	toolCalls      []string // 已调用的工具列表
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

	// 添加步骤信息
	if s.stepInfo != "" {
		parts = append(parts, fmt.Sprintf("📍 %s", s.stepInfo))
	}

	// 添加工具调用信息
	if len(s.toolCalls) > 0 {
		toolsStr := strings.Join(s.toolCalls, " → ")
		parts = append(parts, fmt.Sprintf("🔧 %s", toolsStr))
	}

	// 添加迭代信息
	if s.iteration > 0 {
		parts = append(parts, fmt.Sprintf("🔄 第 %d 轮", s.iteration))
	}

	// 分隔线
	if len(parts) > 0 && s.currentContent != "" {
		parts = append(parts, "\n"+strings.Repeat("─", 20)+"\n")
	}

	// 添加主要内容
	if s.currentContent != "" {
		parts = append(parts, s.currentContent)
	}

	return strings.Join(parts, "\n")
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
	placeholders sync.Map // chatID -> messageID
	stopThinking sync.Map // chatID -> thinkingCancel
	streamStates sync.Map // chatID -> *StreamState
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

	// 启动流式消息处理协程
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

// handleStreamMessages 处理流式消息事件
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

			// 只处理 telegram 通道的消息
			if msg.Channel != "telegram" {
				continue
			}

			c.handleStreamEvent(ctx, msg)
		}
	}
}

// handleStreamEvent 处理单个流式事件
func (c *TelegramChannel) handleStreamEvent(ctx context.Context, msg bus.StreamMessage) {
	chatID, err := parseChatID(msg.ChatID)
	if err != nil {
		logger.ErrorCF("telegram", "Invalid chat ID in stream event", map[string]interface{}{
			"chat_id": msg.ChatID,
		})
		return
	}

	// 获取或创建流式状态
	stateInterface, _ := c.streamStates.LoadOrStore(msg.ChatID, &StreamState{})
	state := stateInterface.(*StreamState)

	switch msg.Type {
	case bus.StreamEventThinking:
		state.SetStep(msg.Content)
		state.SetActive(true)
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventToolCall:
		state.AddToolCall(msg.ToolName)
		state.iteration = msg.Iteration
		c.updateStreamMessage(ctx, chatID, state)

	case bus.StreamEventToolResult:
		// 工具结果，更新状态但不立即刷新显示
		state.SetStep("")

	case bus.StreamEventContent:
		state.AppendContent(msg.Content)
		state.iteration = msg.Iteration
		// 限制更新频率，避免频繁编辑消息
		if time.Since(state.lastUpdate) > 500*time.Millisecond {
			c.updateStreamMessage(ctx, chatID, state)
		}

	case bus.StreamEventComplete:
		// 流式响应完成
		state.UpdateContent(msg.Content)
		state.SetActive(false)
		state.SetStep("")
		c.finalizeStreamMessage(ctx, chatID, state)
		// 清理状态
		c.streamStates.Delete(msg.ChatID)

	case bus.StreamEventError:
		state.UpdateContent(fmt.Sprintf("❌ 错误: %s", msg.Content))
		state.SetActive(false)
		c.finalizeStreamMessage(ctx, chatID, state)
		c.streamStates.Delete(msg.ChatID)
	}
}

// updateStreamMessage 更新流式消息（用于思考中和内容生成过程）
func (c *TelegramChannel) updateStreamMessage(ctx context.Context, chatID int64, state *StreamState) {
	content := state.GetDisplayContent()
	if content == "" {
		return
	}

	htmlContent := markdownToTelegramHTML(content)

	// 尝试编辑现有消息
	if state.messageID != 0 {
		editMsg := tu.EditMessageText(tu.ID(chatID), state.messageID, htmlContent)
		editMsg.ParseMode = telego.ModeHTML
		if _, err := c.bot.EditMessageText(ctx, editMsg); err != nil {
			// 编辑失败，尝试发送新消息
			c.sendNewStreamMessage(ctx, chatID, state, htmlContent)
		}
	} else {
		c.sendNewStreamMessage(ctx, chatID, state, htmlContent)
	}
}

// sendNewStreamMessage 发送新的流式消息
func (c *TelegramChannel) sendNewStreamMessage(ctx context.Context, chatID int64, state *StreamState, htmlContent string) {
	msg := tu.Message(tu.ID(chatID), htmlContent)
	msg.ParseMode = telego.ModeHTML

	sentMsg, err := c.bot.SendMessage(ctx, msg)
	if err != nil {
		// HTML 解析失败，尝试纯文本
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

// finalizeStreamMessage 完成流式消息，发送最终内容
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

	// 检查白名单
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

	// 发送打字状态
	err := c.bot.SendChatAction(ctx, tu.ChatAction(tu.ID(chatID), telego.ChatActionTyping))
	if err != nil {
		logger.ErrorCF("telegram", "Failed to send chat action", map[string]interface{}{
			"error": err.Error(),
		})
	}

	// 停止之前的思考动画
	chatIDStr := fmt.Sprintf("%d", chatID)
	if prevStop, ok := c.stopThinking.Load(chatIDStr); ok {
		if cf, ok := prevStop.(*thinkingCancel); ok && cf != nil {
			cf.Cancel()
		}
	}

	// 创建取消函数用于思考状态
	_, thinkCancel := context.WithTimeout(ctx, 5*time.Minute)
	c.stopThinking.Store(chatIDStr, &thinkingCancel{fn: thinkCancel})

	// 发送占位消息
	pMsg, err := c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), "🤔 正在思考..."))
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

	// 使用流式模式（如果配置启用）
	c.handleMessageWithStream(senderID, fmt.Sprintf("%d", chatID), content, mediaPaths, metadata)
}

// handleMessageWithStream 处理消息，支持流式模式
func (c *TelegramChannel) handleMessageWithStream(senderID, chatID, content string, media []string, metadata map[string]string) {
	if !c.IsAllowed(senderID) {
		return
	}

	// 构建会话密钥
	sessionKey := fmt.Sprintf("%s:%s", c.Name(), chatID)

	msg := bus.InboundMessage{
		Channel:    c.Name(),
		SenderID:   senderID,
		ChatID:     chatID,
		Content:    content,
		Media:      media,
		SessionKey: sessionKey,
		Metadata:   metadata,
		StreamMode: c.config.StreamMode, // 根据配置启用流式模式
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

	// 停止思考动画
	if stop, ok := c.stopThinking.Load(msg.ChatID); ok {
		if cf, ok := stop.(*thinkingCancel); ok && cf != nil {
			cf.Cancel()
		}
		c.stopThinking.Delete(msg.ChatID)
	}

	htmlContent := markdownToTelegramHTML(msg.Content)

	// 尝试编辑占位消息
	if pID, ok := c.placeholders.Load(msg.ChatID); ok {
		c.placeholders.Delete(msg.ChatID)
		editMsg := tu.EditMessageText(tu.ID(chatID), pID.(int), htmlContent)
		editMsg.ParseMode = telego.ModeHTML

		if _, err = c.bot.EditMessageText(ctx, editMsg); err == nil {
			return nil
		}
		// 编辑失败则发送新消息
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

	text = regexp.MustCompile(`^[-*]\s+`).ReplaceAllString(text, "• ")

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

// finalizeStreamMessage 完成流式消息，发送最终内容
func (c *TelegramChannel) finalizeStreamMessage(ctx context.Context, chatID int64, state *StreamState) {
	content := state.GetContent()
	if content == "" {
		return
	}

	htmlContent := markdownToTelegramHTML(content)

	// 如果已有消息ID，编辑它
	if state.messageID != 0 {
		editMsg := tu.EditMessageText(tu.ID(chatID), state.messageID, htmlContent)
		editMsg.ParseMode = telego.ModeHTML
		if _, err := c.bot.EditMessageText(ctx, editMsg); err != nil {
			// 编辑失败，删除旧消息并发送新消息
			c.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
				ChatID:    telego.ChatID{ID: chatID},
				MessageID: state.messageID,
			})
			c.sendFinalMessage(ctx, chatID, htmlContent)
		}
	} else {
		c.sendFinalMessage(ctx, chatID, htmlContent)
	}
}

// sendFinalMessage 发送最终消息
func (c *TelegramChannel) sendFinalMessage(ctx context.Context, chatID int64, htmlContent string) {
	msg := tu.Message(tu.ID(chatID), htmlContent)
	msg.ParseMode = telego.ModeHTML

	if _, err := c.bot.SendMessage(ctx, msg); err != nil {
		logger.ErrorCF("telegram", "Failed to send final message", map[string]interface{}{
			"error": err.Error(),
		})
	}
}
