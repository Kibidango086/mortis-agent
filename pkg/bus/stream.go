package bus

// StreamEventType 定义流式事件的类型
type StreamEventType string

const (
	// StreamEventThinking AI正在思考
	StreamEventThinking StreamEventType = "thinking"
	// StreamEventToolCall AI正在调用工具
	StreamEventToolCall StreamEventType = "tool_call"
	// StreamEventToolResult 工具执行结果
	StreamEventToolResult StreamEventType = "tool_result"
	// StreamEventContent 内容片段
	StreamEventContent StreamEventType = "content"
	// StreamEventComplete 流式响应完成
	StreamEventComplete StreamEventType = "complete"
	// StreamEventError 发生错误
	StreamEventError StreamEventType = "error"
)

// StreamMessage 表示流式消息事件
type StreamMessage struct {
	// Channel 目标通道
	Channel string `json:"channel"`
	// ChatID 聊天ID
	ChatID string `json:"chat_id"`
	// SessionKey 会话标识
	SessionKey string `json:"session_key"`
	// Type 事件类型
	Type StreamEventType `json:"type"`
	// Content 内容（根据类型不同含义不同）
	Content string `json:"content"`
	// ToolName 工具名称（仅用于tool_call类型）
	ToolName string `json:"tool_name,omitempty"`
	// ToolArgs 工具参数（仅用于tool_call类型）
	ToolArgs map[string]interface{} `json:"tool_args,omitempty"`
	// Iteration 当前迭代次数
	Iteration int `json:"iteration,omitempty"`
	// IsFinal 是否是最终响应
	IsFinal bool `json:"is_final,omitempty"`
}

// StreamHandler 流式消息处理器接口
type StreamHandler interface {
	// OnStreamEvent 当收到流式事件时调用
	OnStreamEvent(msg StreamMessage)
}

// StreamHandlerFunc 允许将函数作为 StreamHandler
type StreamHandlerFunc func(msg StreamMessage)

func (f StreamHandlerFunc) OnStreamEvent(msg StreamMessage) {
	f(msg)
}

// StreamController 流式控制器接口
type StreamController interface {
	// SendStreamEvent 发送流式事件
	SendStreamEvent(msg StreamMessage)
	// SetStreamHandler 设置流式处理器
	SetStreamHandler(handler StreamHandler)
	// IsStreaming 是否处于流式模式
	IsStreaming() bool
}
