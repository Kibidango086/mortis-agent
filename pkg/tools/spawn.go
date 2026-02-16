package tools

import (
	"context"
	"fmt"
)

type SpawnTool struct {
	manager       *SubagentManager
	originChannel string
	originChatID  string
	callback      AsyncCallback // For async completion notification
}

func NewSpawnTool(manager *SubagentManager) *SpawnTool {
	return &SpawnTool{
		manager:       manager,
		originChannel: "cli",
		originChatID:  "direct",
	}
}

// SetCallback implements AsyncTool interface for async completion notification
func (t *SpawnTool) SetCallback(cb AsyncCallback) {
	t.callback = cb
}

func (t *SpawnTool) Name() string {
	return "spawn"
}

func (t *SpawnTool) Description() string {
	return "Spawn a subagent to handle a task in the background. Use this for complex or time-consuming tasks that can run independently. The subagent will complete the task and report back when done."
}

func (t *SpawnTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type":        "string",
				"description": "The task for subagent to complete",
			},
			"label": map[string]interface{}{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
		},
		"required": []string{"task"},
	}
}

func (t *SpawnTool) SetContext(channel, chatID string) {
	t.originChannel = channel
	t.originChatID = chatID
}

// Execute runs the spawn tool synchronously - it spawns a subagent and waits for completion.
// This ensures the main agent waits for the subagent to finish before continuing.
func (t *SpawnTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	task, ok := args["task"].(string)
	if !ok {
		return ErrorResult("task is required")
	}

	label, _ := args["label"].(string)

	if t.manager == nil {
		return ErrorResult("Subagent manager not configured")
	}

	// Use a channel to wait for completion synchronously
	done := make(chan *ToolResult, 1)

	// Callback to receive subagent result
	callback := func(callbackCtx context.Context, result *ToolResult) {
		select {
		case done <- result:
		default:
		}
	}

	// Spawn the subagent (runs in background)
	_, err := t.manager.Spawn(ctx, task, label, t.originChannel, t.originChatID, callback)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to spawn subagent: %v", err))
	}

	// Wait for subagent to complete (synchronous wait)
	// Use a select to allow context cancellation
	select {
	case result := <-done:
		// Subagent completed, return its result
		return result
	case <-ctx.Done():
		return ErrorResult("spawn cancelled: context expired").WithError(ctx.Err())
	}
}
