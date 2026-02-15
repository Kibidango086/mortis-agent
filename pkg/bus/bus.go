package bus

import (
	"context"
	"sync"
)

// InboundMessage 表示进入系统的消息
type InboundMessage struct {
	Channel    string            `json:"channel"`
	SenderID   string            `json:"sender_id"`
	ChatID     string            `json:"chat_id"`
	Content    string            `json:"content"`
	Media      []string          `json:"media,omitempty"`
	SessionKey string            `json:"session_key"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	// StreamMode 是否启用流式模式
	StreamMode bool `json:"stream_mode,omitempty"`
}

// OutboundMessage 表示从系统发出的消息
type OutboundMessage struct {
	Channel   string `json:"channel"`
	ChatID    string `json:"chat_id"`
	Content   string `json:"content"`
	SessionID string `json:"session_id,omitempty"`
}

// MessageHandler 处理特定通道的消息
type MessageHandler func(ctx context.Context, msg InboundMessage) error

// MessageBus 消息总线，负责系统内部消息传递
type MessageBus struct {
	inbound        chan InboundMessage
	outbound       chan OutboundMessage
	stream         chan StreamMessage
	handlers       map[string]MessageHandler
	streamHandlers sync.Map // chatID -> StreamHandler
	mu             sync.RWMutex
}

// NewMessageBus 创建新的消息总线
func NewMessageBus() *MessageBus {
	return &MessageBus{
		inbound:  make(chan InboundMessage, 100),
		outbound: make(chan OutboundMessage, 100),
		stream:   make(chan StreamMessage, 100),
		handlers: make(map[string]MessageHandler),
	}
}

// PublishInbound 发布进入系统的消息
func (mb *MessageBus) PublishInbound(msg InboundMessage) {
	mb.inbound <- msg
}

// ConsumeInbound 消费进入系统的消息
func (mb *MessageBus) ConsumeInbound(ctx context.Context) (InboundMessage, bool) {
	select {
	case msg := <-mb.inbound:
		return msg, true
	case <-ctx.Done():
		return InboundMessage{}, false
	}
}

// PublishOutbound 发布从系统发出的消息
func (mb *MessageBus) PublishOutbound(msg OutboundMessage) {
	mb.outbound <- msg
}

// SubscribeOutbound 订阅出站消息
func (mb *MessageBus) SubscribeOutbound(ctx context.Context) (OutboundMessage, bool) {
	select {
	case msg := <-mb.outbound:
		return msg, true
	case <-ctx.Done():
		return OutboundMessage{}, false
	}
}

// PublishStream 发布流式消息事件
func (mb *MessageBus) PublishStream(msg StreamMessage) {
	mb.stream <- msg

	// 同时通知注册的流式处理器
	if handler, ok := mb.streamHandlers.Load(msg.ChatID); ok {
		if h, ok := handler.(StreamHandler); ok {
			h.OnStreamEvent(msg)
		}
	}
}

// SubscribeStream 订阅流式消息事件
func (mb *MessageBus) SubscribeStream(ctx context.Context) (StreamMessage, bool) {
	select {
	case msg := <-mb.stream:
		return msg, true
	case <-ctx.Done():
		return StreamMessage{}, false
	}
}

// RegisterStreamHandler 为特定 chatID 注册流式处理器
func (mb *MessageBus) RegisterStreamHandler(chatID string, handler StreamHandler) {
	mb.streamHandlers.Store(chatID, handler)
}

// UnregisterStreamHandler 注销流式处理器
func (mb *MessageBus) UnregisterStreamHandler(chatID string) {
	mb.streamHandlers.Delete(chatID)
}

// GetStreamHandler 获取指定 chatID 的流式处理器
func (mb *MessageBus) GetStreamHandler(chatID string) (StreamHandler, bool) {
	if handler, ok := mb.streamHandlers.Load(chatID); ok {
		if h, ok := handler.(StreamHandler); ok {
			return h, true
		}
	}
	return nil, false
}

// RegisterHandler 注册通道处理器
func (mb *MessageBus) RegisterHandler(channel string, handler MessageHandler) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.handlers[channel] = handler
}

// GetHandler 获取通道处理器
func (mb *MessageBus) GetHandler(channel string) (MessageHandler, bool) {
	mb.mu.RLock()
	defer mb.mu.RUnlock()
	handler, ok := mb.handlers[channel]
	return handler, ok
}

// Close 关闭消息总线
func (mb *MessageBus) Close() {
	close(mb.inbound)
	close(mb.outbound)
	close(mb.stream)
}
