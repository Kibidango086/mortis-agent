package bus

import "time"

type StreamEventType string

const (
	// Text events - 1:1 from opencode
	StreamEventTextStart StreamEventType = "text-start"
	StreamEventTextDelta StreamEventType = "text-delta"
	StreamEventTextEnd   StreamEventType = "text-end"

	// Reasoning events - 1:1 from opencode
	StreamEventReasoningStart StreamEventType = "reasoning-start"
	StreamEventReasoningDelta StreamEventType = "reasoning-delta"
	StreamEventReasoningEnd   StreamEventType = "reasoning-end"

	// Tool events - 1:1 from opencode
	StreamEventToolInputStart StreamEventType = "tool-input-start"
	StreamEventToolInputDelta StreamEventType = "tool-input-delta"
	StreamEventToolInputEnd   StreamEventType = "tool-input-end"
	StreamEventToolCall       StreamEventType = "tool-call"
	StreamEventToolResult     StreamEventType = "tool-result"
	StreamEventToolError      StreamEventType = "tool-error"

	// Step events - 1:1 from opencode
	StreamEventStartStep  StreamEventType = "start-step"
	StreamEventFinishStep StreamEventType = "finish-step"

	// Lifecycle events - 1:1 from opencode
	StreamEventStart  StreamEventType = "start"
	StreamEventFinish StreamEventType = "finish"
	StreamEventError  StreamEventType = "error"

	// Legacy events (保留向后兼容)
	StreamEventThinking         StreamEventType = "thinking"
	StreamEventToolResultLegacy StreamEventType = "tool_result"
	StreamEventContent          StreamEventType = "content"
	StreamEventComplete         StreamEventType = "complete"
)

type StreamMessage struct {
	Channel    string          `json:"channel"`
	ChatID     string          `json:"chat_id"`
	SessionKey string          `json:"session_key"`
	Type       StreamEventType `json:"type"`
	Content    string          `json:"content"`

	// For text/reasoning events (opencode 模式)
	ID     string `json:"id,omitempty"`
	Delta  string `json:"delta,omitempty"`
	PartID string `json:"part_id,omitempty"`

	// For tool events (opencode 模式)
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolArgs   map[string]interface{} `json:"tool_args,omitempty"`
	ToolResult string                 `json:"tool_result,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolInput  map[string]interface{} `json:"tool_input,omitempty"`
	Error      string                 `json:"error,omitempty"`

	// For step events (opencode 模式)
	Iteration    int    `json:"iteration,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`

	// For finish-step (opencode 模式)
	Usage struct {
		PromptTokens     int `json:"prompt_tokens,omitempty"`
		CompletionTokens int `json:"completion_tokens,omitempty"`
		TotalTokens      int `json:"total_tokens,omitempty"`
	} `json:"usage,omitempty"`

	// Legacy fields (保留向后兼容)
	IsFinal   bool      `json:"is_final,omitempty"`
	Timestamp time.Time `json:"timestamp"`
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
