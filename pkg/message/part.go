// Package message implements the Part-based message system inspired by opencode
package message

import (
	"time"
)

// PartType represents the type of a message part
type PartType string

const (
	PartTypeText       PartType = "text"
	PartTypeTool       PartType = "tool"
	PartTypeReasoning  PartType = "reasoning"
	PartTypeStepStart  PartType = "step-start"
	PartTypeStepFinish PartType = "step-finish"
	PartTypeError      PartType = "error"
)

// Part is the base interface for all message parts
type Part struct {
	ID        string                 `json:"id"`
	SessionID string                 `json:"sessionID"`
	MessageID string                 `json:"messageID"`
	Type      PartType               `json:"type"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// TextPart represents text content from the assistant
type TextPart struct {
	Part
	Text      string     `json:"text"`
	Synthetic bool       `json:"synthetic,omitempty"`
	Ignored   bool       `json:"ignored,omitempty"`
	Time      *TimeRange `json:"time,omitempty"`
}

// TimeRange represents a time range
type TimeRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end,omitempty"`
}

// ToolState represents the state of a tool execution
type ToolState string

const (
	ToolStatePending   ToolState = "pending"
	ToolStateRunning   ToolState = "running"
	ToolStateCompleted ToolState = "completed"
	ToolStateError     ToolState = "error"
)

// ToolStatePendingData represents pending tool state
type ToolStatePendingData struct {
	Status ToolState              `json:"status"`
	Input  map[string]interface{} `json:"input"`
	Raw    string                 `json:"raw"`
}

// ToolStateRunningData represents running tool state
type ToolStateRunningData struct {
	Status   ToolState              `json:"status"`
	Input    map[string]interface{} `json:"input"`
	Title    string                 `json:"title,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Time     struct {
		Start int64 `json:"start"`
	} `json:"time"`
}

// ToolStateCompletedData represents completed tool state
type ToolStateCompletedData struct {
	Status   ToolState              `json:"status"`
	Input    map[string]interface{} `json:"input"`
	Output   string                 `json:"output"`
	Title    string                 `json:"title"`
	Metadata map[string]interface{} `json:"metadata"`
	Time     struct {
		Start     int64 `json:"start"`
		End       int64 `json:"end"`
		Compacted int64 `json:"compacted,omitempty"`
	} `json:"time"`
}

// ToolStateErrorData represents error tool state
type ToolStateErrorData struct {
	Status   ToolState              `json:"status"`
	Input    map[string]interface{} `json:"input"`
	Error    string                 `json:"error"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Time     struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	} `json:"time"`
}

// ToolPart represents a tool call
type ToolPart struct {
	Part
	CallID string      `json:"callID"`
	Tool   string      `json:"tool"`
	State  interface{} `json:"state"` // Can be ToolStatePendingData, ToolStateRunningData, ToolStateCompletedData, or ToolStateErrorData
}

// ReasoningPart represents reasoning content
type ReasoningPart struct {
	Part
	Text     string                 `json:"text"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Time     TimeRange              `json:"time"`
}

// StepStartPart represents the start of a step
type StepStartPart struct {
	Part
	Snapshot string `json:"snapshot,omitempty"`
}

// StepFinishPart represents the end of a step
type StepFinishPart struct {
	Part
	Reason   string  `json:"reason"`
	Snapshot string  `json:"snapshot,omitempty"`
	Cost     float64 `json:"cost"`
	Tokens   struct {
		Total     int `json:"total,omitempty"`
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

// UserMessage represents a user message
type UserMessage struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Time      struct {
		Created int64 `json:"created"`
	} `json:"time"`
}

// AssistantMessage represents an assistant message
type AssistantMessage struct {
	ID        string  `json:"id"`
	SessionID string  `json:"sessionID"`
	Role      string  `json:"role"`
	Agent     string  `json:"agent"`
	Parts     []Part  `json:"parts"`
	Finish    string  `json:"finish,omitempty"`
	Cost      float64 `json:"cost"`
	Tokens    int     `json:"tokens"`
	Error     *Error  `json:"error,omitempty"`
}

// Error represents an error
type Error struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// FilePart represents a file attachment
type FilePart struct {
	Part
	Mime     string `json:"mime"`
	Filename string `json:"filename,omitempty"`
	URL      string `json:"url"`
}

// ExecutionStep represents a step in the execution log
type ExecutionStep struct {
	Type       string                 `json:"type"`
	Timestamp  time.Time              `json:"timestamp"`
	Content    string                 `json:"content,omitempty"`
	ToolName   string                 `json:"toolName,omitempty"`
	ToolArgs   map[string]interface{} `json:"toolArgs,omitempty"`
	ToolResult string                 `json:"toolResult,omitempty"`
	Iteration  int                    `json:"iteration,omitempty"`
}

// ExecutionLog represents the execution log
type ExecutionLog struct {
	Steps     []ExecutionStep `json:"steps"`
	StartTime time.Time       `json:"startTime"`
	EndTime   time.Time       `json:"endTime"`
}

// Message is a union type for all message types
type Message interface {
	GetID() string
	GetSessionID() string
	GetRole() string
}

func (u UserMessage) GetID() string        { return u.ID }
func (u UserMessage) GetSessionID() string { return u.SessionID }
func (u UserMessage) GetRole() string      { return u.Role }

func (a AssistantMessage) GetID() string        { return a.ID }
func (a AssistantMessage) GetSessionID() string { return a.SessionID }
func (a AssistantMessage) GetRole() string      { return a.Role }

// PartUpdate represents an update to a part
type PartUpdate struct {
	SessionID string
	MessageID string
	PartID    string
	Field     string
	Delta     string
}

// NewTextPart creates a new text part
func NewTextPart(sessionID, messageID, id, text string) *TextPart {
	return &TextPart{
		Part: Part{
			ID:        id,
			SessionID: sessionID,
			MessageID: messageID,
			Type:      PartTypeText,
		},
		Text: text,
		Time: &TimeRange{
			Start: time.Now().UnixMilli(),
		},
	}
}

// NewToolPart creates a new tool part
func NewToolPart(sessionID, messageID, id, callID, toolName string) *ToolPart {
	return &ToolPart{
		Part: Part{
			ID:        id,
			SessionID: sessionID,
			MessageID: messageID,
			Type:      PartTypeTool,
		},
		CallID: callID,
		Tool:   toolName,
		State: ToolStatePendingData{
			Status: ToolStatePending,
			Input:  make(map[string]interface{}),
			Raw:    "",
		},
	}
}

// UpdateToolState updates the tool state
func (t *ToolPart) UpdateToolState(state interface{}) {
	t.State = state
}

// GetToolState returns the current tool state type
func (t *ToolPart) GetToolState() ToolState {
	switch s := t.State.(type) {
	case ToolStatePendingData:
		return s.Status
	case ToolStateRunningData:
		return s.Status
	case ToolStateCompletedData:
		return s.Status
	case ToolStateErrorData:
		return s.Status
	default:
		return ToolStatePending
	}
}
