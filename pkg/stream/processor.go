// Package stream implements stream processing inspired by opencode
package stream

import (
	"fmt"
	"strings"
	"time"

	"github.com/Kibidango086/mortis-agent/pkg/message"
)

// EventType represents stream event types
type EventType string

const (
	// Lifecycle events
	EventStart  EventType = "start"
	EventFinish EventType = "finish"
	EventError  EventType = "error"

	// Text events
	EventTextStart EventType = "text-start"
	EventTextDelta EventType = "text-delta"
	EventTextEnd   EventType = "text-end"

	// Reasoning events
	EventReasoningStart EventType = "reasoning-start"
	EventReasoningDelta EventType = "reasoning-delta"
	EventReasoningEnd   EventType = "reasoning-end"

	// Tool events
	EventToolInputStart EventType = "tool-input-start"
	EventToolInputDelta EventType = "tool-input-delta"
	EventToolInputEnd   EventType = "tool-input-end"
	EventToolCall       EventType = "tool-call"
	EventToolResult     EventType = "tool-result"
	EventToolError      EventType = "tool-error"

	// Step events
	EventStepStart  EventType = "step-start"
	EventStepFinish EventType = "step-finish"
)

// Event represents a stream event
type Event struct {
	Type             EventType              `json:"type"`
	ID               string                 `json:"id,omitempty"`
	Delta            string                 `json:"delta,omitempty"`
	PartID           string                 `json:"partID,omitempty"`
	ToolCallID       string                 `json:"toolCallID,omitempty"`
	ToolName         string                 `json:"toolName,omitempty"`
	ToolInput        map[string]interface{} `json:"toolInput,omitempty"`
	ToolResult       string                 `json:"toolResult,omitempty"`
	ToolOutput       *ToolOutput            `json:"toolOutput,omitempty"`
	Error            error                  `json:"error,omitempty"`
	Usage            *Usage                 `json:"usage,omitempty"`
	FinishReason     string                 `json:"finishReason,omitempty"`
	ProviderMetadata map[string]interface{} `json:"providerMetadata,omitempty"`
}

// ToolOutput represents tool execution output
type ToolOutput struct {
	Output      string                 `json:"output"`
	Title       string                 `json:"title"`
	Metadata    map[string]interface{} `json:"metadata"`
	Attachments []*message.FilePart    `json:"attachments,omitempty"`
}

// Usage represents token usage
type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// Processor handles stream events and manages state
type Processor struct {
	SessionID    string
	MessageID    string
	CurrentText  *message.TextPart
	ReasoningMap map[string]*message.ReasoningPart
	ToolCalls    map[string]*message.ToolPart
	ToolCallList []string
	Parts        []interface{} // All parts in order
}

// NewProcessor creates a new stream processor
func NewProcessor(sessionID, messageID string) *Processor {
	return &Processor{
		SessionID:    sessionID,
		MessageID:    messageID,
		ReasoningMap: make(map[string]*message.ReasoningPart),
		ToolCalls:    make(map[string]*message.ToolPart),
		ToolCallList: make([]string, 0),
		Parts:        make([]interface{}, 0),
	}
}

// ProcessEvent processes a single stream event
func (p *Processor) ProcessEvent(event Event) (interface{}, error) {
	switch event.Type {
	case EventTextStart:
		return p.handleTextStart(event)
	case EventTextDelta:
		return p.handleTextDelta(event)
	case EventTextEnd:
		return p.handleTextEnd(event)
	case EventReasoningStart:
		return p.handleReasoningStart(event)
	case EventReasoningDelta:
		return p.handleReasoningDelta(event)
	case EventReasoningEnd:
		return p.handleReasoningEnd(event)
	case EventToolInputStart:
		return p.handleToolInputStart(event)
	case EventToolCall:
		return p.handleToolCall(event)
	case EventToolResult:
		return p.handleToolResult(event)
	case EventToolError:
		return p.handleToolError(event)
	case EventStepStart:
		return p.handleStepStart(event)
	case EventStepFinish:
		return p.handleStepFinish(event)
	case EventStart, EventFinish:
		// Lifecycle events - no parts created
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown event type: %s", event.Type)
	}
}

func (p *Processor) handleTextStart(event Event) (*message.TextPart, error) {
	part := &message.TextPart{
		Part: message.Part{
			ID:        event.PartID,
			SessionID: p.SessionID,
			MessageID: p.MessageID,
			Type:      message.PartTypeText,
		},
		Text: "",
		Time: &message.TimeRange{
			Start: time.Now().UnixMilli(),
		},
	}
	p.CurrentText = part
	p.Parts = append(p.Parts, part)
	return part, nil
}

func (p *Processor) handleTextDelta(event Event) (*message.TextPart, error) {
	if p.CurrentText == nil {
		return nil, fmt.Errorf("text-delta without text-start")
	}
	p.CurrentText.Text += event.Delta
	return p.CurrentText, nil
}

func (p *Processor) handleTextEnd(event Event) (*message.TextPart, error) {
	if p.CurrentText == nil {
		return nil, nil
	}
	p.CurrentText.Text = trimEnd(p.CurrentText.Text)
	p.CurrentText.Time.End = time.Now().UnixMilli()
	result := p.CurrentText
	p.CurrentText = nil
	return result, nil
}

func (p *Processor) handleReasoningStart(event Event) (*message.ReasoningPart, error) {
	if _, exists := p.ReasoningMap[event.ID]; exists {
		return nil, nil
	}
	part := &message.ReasoningPart{
		Part: message.Part{
			ID:        event.PartID,
			SessionID: p.SessionID,
			MessageID: p.MessageID,
			Type:      message.PartTypeReasoning,
			Metadata:  event.ProviderMetadata,
		},
		Text: "",
		Time: message.TimeRange{
			Start: time.Now().UnixMilli(),
		},
	}
	p.ReasoningMap[event.ID] = part
	p.Parts = append(p.Parts, part)
	return part, nil
}

func (p *Processor) handleReasoningDelta(event Event) (*message.ReasoningPart, error) {
	part, exists := p.ReasoningMap[event.ID]
	if !exists {
		return nil, nil
	}
	part.Text += event.Delta
	if event.ProviderMetadata != nil {
		part.Metadata = event.ProviderMetadata
	}
	return part, nil
}

func (p *Processor) handleReasoningEnd(event Event) (*message.ReasoningPart, error) {
	part, exists := p.ReasoningMap[event.ID]
	if !exists {
		return nil, nil
	}
	part.Text = trimEnd(part.Text)
	part.Time.End = time.Now().UnixMilli()
	if event.ProviderMetadata != nil {
		part.Metadata = event.ProviderMetadata
	}
	delete(p.ReasoningMap, event.ID)
	return part, nil
}

func (p *Processor) handleToolInputStart(event Event) (*message.ToolPart, error) {
	existing, exists := p.ToolCalls[event.ID]
	if exists {
		return existing, nil
	}

	part := &message.ToolPart{
		Part: message.Part{
			ID:        event.PartID,
			SessionID: p.SessionID,
			MessageID: p.MessageID,
			Type:      message.PartTypeTool,
			Metadata:  event.ProviderMetadata,
		},
		CallID: event.ToolCallID,
		Tool:   event.ToolName,
		State: message.ToolStatePendingData{
			Status: message.ToolStatePending,
			Input:  make(map[string]interface{}),
			Raw:    "",
		},
	}
	p.ToolCalls[event.ToolCallID] = part
	p.ToolCallList = append(p.ToolCallList, event.ToolCallID)
	p.Parts = append(p.Parts, part)
	return part, nil
}

func (p *Processor) handleToolCall(event Event) (*message.ToolPart, error) {
	part, exists := p.ToolCalls[event.ToolCallID]
	if !exists {
		return nil, fmt.Errorf("tool-call without tool-input-start")
	}

	part.Tool = event.ToolName
	part.State = message.ToolStateRunningData{
		Status:   message.ToolStateRunning,
		Input:    event.ToolInput,
		Metadata: event.ProviderMetadata,
		Time: struct {
			Start int64 `json:"start"`
		}{
			Start: time.Now().UnixMilli(),
		},
	}
	return part, nil
}

func (p *Processor) handleToolResult(event Event) (*message.ToolPart, error) {
	part, exists := p.ToolCalls[event.ToolCallID]
	if !exists {
		return nil, nil
	}

	runningState, ok := part.State.(message.ToolStateRunningData)
	if !ok {
		return nil, fmt.Errorf("tool-result but state is not running")
	}

	output := event.ToolOutput
	if output == nil {
		output = &ToolOutput{
			Output:   event.ToolResult,
			Title:    event.ToolName,
			Metadata: make(map[string]interface{}),
		}
	}

	part.State = message.ToolStateCompletedData{
		Status:   message.ToolStateCompleted,
		Input:    event.ToolInput,
		Output:   output.Output,
		Title:    output.Title,
		Metadata: output.Metadata,
		Time: struct {
			Start     int64 `json:"start"`
			End       int64 `json:"end"`
			Compacted int64 `json:"compacted,omitempty"`
		}{
			Start: runningState.Time.Start,
			End:   time.Now().UnixMilli(),
		},
	}

	delete(p.ToolCalls, event.ToolCallID)
	return part, nil
}

func (p *Processor) handleToolError(event Event) (*message.ToolPart, error) {
	part, exists := p.ToolCalls[event.ToolCallID]
	if !exists {
		return nil, nil
	}

	runningState, ok := part.State.(message.ToolStateRunningData)
	if !ok {
		// Try to get start time from pending state
		if _, ok := part.State.(message.ToolStatePendingData); ok {
			part.State = message.ToolStateErrorData{
				Status: message.ToolStateError,
				Input:  event.ToolInput,
				Error:  event.Error.Error(),
				Time: struct {
					Start int64 `json:"start"`
					End   int64 `json:"end"`
				}{
					Start: time.Now().UnixMilli(),
					End:   time.Now().UnixMilli(),
				},
			}
			delete(p.ToolCalls, event.ToolCallID)
			return part, nil
		}
		return nil, fmt.Errorf("tool-error but state is not running or pending")
	}

	part.State = message.ToolStateErrorData{
		Status: message.ToolStateError,
		Input:  event.ToolInput,
		Error:  event.Error.Error(),
		Time: struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		}{
			Start: runningState.Time.Start,
			End:   time.Now().UnixMilli(),
		},
	}

	delete(p.ToolCalls, event.ToolCallID)
	return part, nil
}

func (p *Processor) handleStepStart(event Event) (*message.StepStartPart, error) {
	part := &message.StepStartPart{
		Part: message.Part{
			ID:        event.PartID,
			SessionID: p.SessionID,
			MessageID: p.MessageID,
			Type:      message.PartTypeStepStart,
		},
	}
	p.Parts = append(p.Parts, part)
	return part, nil
}

func (p *Processor) handleStepFinish(event Event) (*message.StepFinishPart, error) {
	part := &message.StepFinishPart{
		Part: message.Part{
			ID:        event.PartID,
			SessionID: p.SessionID,
			MessageID: p.MessageID,
			Type:      message.PartTypeStepFinish,
		},
		Reason: event.FinishReason,
	}
	if event.Usage != nil {
		part.Tokens.Total = event.Usage.TotalTokens
		part.Tokens.Input = event.Usage.PromptTokens
		part.Tokens.Output = event.Usage.CompletionTokens
	}
	p.Parts = append(p.Parts, part)
	return part, nil
}

// GetParts returns all parts
func (p *Processor) GetParts() []interface{} {
	return p.Parts
}

// GetToolCalls returns completed tool calls for View Details
func (p *Processor) GetToolCalls() []message.ExecutionStep {
	var steps []message.ExecutionStep
	for _, part := range p.Parts {
		if toolPart, ok := part.(*message.ToolPart); ok {
			step := message.ExecutionStep{
				Type:     "tool_call",
				ToolName: toolPart.Tool,
			}

			switch state := toolPart.State.(type) {
			case message.ToolStateRunningData:
				step.ToolArgs = state.Input
				step.Timestamp = time.UnixMilli(state.Time.Start)
			case message.ToolStateCompletedData:
				step.ToolArgs = state.Input
				step.ToolResult = state.Output
				step.Timestamp = time.UnixMilli(state.Time.Start)
			case message.ToolStateErrorData:
				step.ToolArgs = state.Input
				step.ToolResult = "Error: " + state.Error
				step.Timestamp = time.UnixMilli(state.Time.Start)
			}

			steps = append(steps, step)
		}
	}
	return steps
}

// GetFinalText returns the final text content
func (p *Processor) GetFinalText() string {
	for _, part := range p.Parts {
		if textPart, ok := part.(*message.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

// HasToolCalls returns true if there are any tool calls
func (p *Processor) HasToolCalls() bool {
	for _, part := range p.Parts {
		if _, ok := part.(*message.ToolPart); ok {
			return true
		}
	}
	return false
}

func trimEnd(s string) string {
	return trimRight(s, " \n\t\r")
}

func trimRight(s string, cutset string) string {
	for len(s) > 0 && strings.Contains(cutset, string(s[len(s)-1])) {
		s = s[:len(s)-1]
	}
	return s
}
