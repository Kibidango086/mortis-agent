package bus

import "time"

type StreamEventType string

const (
	StreamEventThinking   StreamEventType = "thinking"
	StreamEventToolCall   StreamEventType = "tool_call"
	StreamEventToolResult StreamEventType = "tool_result"
	StreamEventContent    StreamEventType = "content"
	StreamEventComplete   StreamEventType = "complete"
	StreamEventError      StreamEventType = "error"
)

type StreamMessage struct {
	Channel    string                 `json:"channel"`
	ChatID     string                 `json:"chat_id"`
	SessionKey string                 `json:"session_key"`
	Type       StreamEventType        `json:"type"`
	Content    string                 `json:"content"`
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolArgs   map[string]interface{} `json:"tool_args,omitempty"`
	ToolResult string                 `json:"tool_result,omitempty"`
	Iteration  int                    `json:"iteration,omitempty"`
	IsFinal    bool                   `json:"is_final,omitempty"`
	Timestamp  time.Time              `json:"timestamp"`
}

type StreamHandler interface {
	OnStreamEvent(msg StreamMessage)
}

type StreamHandlerFunc func(msg StreamMessage)

func (f StreamHandlerFunc) OnStreamEvent(msg StreamMessage) {
	f(msg)
}

type StreamController interface {
	SendStreamEvent(msg StreamMessage)
	SetStreamHandler(handler StreamHandler)
	IsStreaming() bool
}

type ExecutionLog struct {
	Steps     []ExecutionStep `json:"steps"`
	StartTime time.Time       `json:"start_time"`
	EndTime   time.Time       `json:"end_time"`
}

type ExecutionStep struct {
	Type       string                 `json:"type"`
	Timestamp  time.Time              `json:"timestamp"`
	Content    string                 `json:"content,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolArgs   map[string]interface{} `json:"tool_args,omitempty"`
	ToolResult string                 `json:"tool_result,omitempty"`
	Iteration  int                    `json:"iteration,omitempty"`
	Duration   time.Duration          `json:"duration,omitempty"`
}

func NewStreamMessage(eventType StreamEventType, content string) StreamMessage {
	return StreamMessage{
		Type:      eventType,
		Content:   content,
		Timestamp: time.Now(),
	}
}
