package tools

import (
	"context"
	"fmt"
	"sync"
)

type SpawnTool struct {
	manager       *SubagentManager
	originChannel string
	originChatID  string
}

func NewSpawnTool(manager *SubagentManager) *SpawnTool {
	return &SpawnTool{
		manager:       manager,
		originChannel: "cli",
		originChatID:  "direct",
	}
}

func (t *SpawnTool) Name() string {
	return "spawn"
}

func (t *SpawnTool) Description() string {
	return `Spawn multiple subagents in parallel to handle independent tasks. 
Each subagent runs concurrently and results are returned after all complete.
- Use "tasks" array to spawn multiple subagents at once
- Each task needs a unique "label" for identification
- All subagents run in parallel and wait for all to finish before returning
- Perfect for: parallel searches, multiple file operations, dividing complex tasks`
}

func (t *SpawnTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"tasks": map[string]interface{}{
				"type": "array",
				"description": "Array of tasks to spawn. Each task will run in parallel.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"task": map[string]interface{}{
							"type":        "string",
							"description": "The task for subagent to complete",
						},
						"label": map[string]interface{}{
							"type":        "string",
							"description": "Unique label to identify this task result",
						},
					},
					"required": []string{"task", "label"},
				},
			},
		},
		"required": []string{"tasks"},
	}
}

func (t *SpawnTool) SetContext(channel, chatID string) {
	t.originChannel = channel
	t.originChatID = chatID
}

// TaskSpec defines a single task for spawn
type TaskSpec struct {
	Task  string `json:"task"`
	Label string `json:"label"`
}

// Execute spawns multiple subagents in parallel and waits for all to complete.
// Returns results for all tasks in a structured format.
func (t *SpawnTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	// Extract tasks array
	tasksIface, ok := args["tasks"]
	if !ok {
		// Backward compatibility: also check for single "task" field
		task, hasTask := args["task"].(string)
		label, _ := args["label"].(string)
		if hasTask {
			tasksIface = []interface{}{
				map[string]interface{}{"task": task, "label": label},
			}
		} else {
			return ErrorResult("tasks array is required")
		}
	}

	tasksArr, ok := tasksIface.([]interface{})
	if !ok || len(tasksArr) == 0 {
		return ErrorResult("tasks must be a non-empty array")
	}

	if t.manager == nil {
		return ErrorResult("Subagent manager not configured")
	}

	// Parse tasks
	tasks := make([]TaskSpec, 0, len(tasksArr))
	for i, t := range tasksArr {
		taskMap, ok := t.(map[string]interface{})
		if !ok {
			return ErrorResult(fmt.Sprintf("tasks[%d] must be an object", i))
		}
		taskStr, _ := taskMap["task"].(string)
		labelStr, _ := taskMap["label"].(string)
		if taskStr == "" {
			return ErrorResult(fmt.Sprintf("tasks[%d].task is required", i))
		}
		if labelStr == "" {
			labelStr = fmt.Sprintf("task-%d", i)
		}
		tasks = append(tasks, TaskSpec{Task: taskStr, Label: labelStr})
	}

	// Spawn all subagents in parallel and collect results
	results := make([]struct {
		Label    string
		Result   string
		IsError  bool
		Error    string
	}, len(tasks))

	var wg sync.WaitGroup
	var mu sync.Mutex

	// Use a cancellable context for all subagents
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i, taskSpec := range tasks {
		wg.Add(1)
		go func(index int, task, label string) {
			defer wg.Done()

			result := t.manager.RunSubagentSync(subCtx, task, label, t.originChannel, t.originChatID)

			mu.Lock()
			results[index] = struct {
				Label    string
				Result   string
				IsError  bool
				Error    string
			}{
				Label:   label,
				Result:  result,
				IsError: result == "",
			}
			mu.Unlock()
		}(i, taskSpec.Task, taskSpec.Label)
	}

	// Wait for all subagents to complete
	wg.Wait()

	// Build result summary
	summary := fmt.Sprintf("Spawned %d subagent(s) in parallel:\n\n", len(tasks))
	for _, r := range results {
		if r.IsError {
			summary += fmt.Sprintf("❌ %s: %s\n", r.Label, r.Error)
		} else {
			// Truncate long results for summary
			resultPreview := r.Result
			if len(resultPreview) > 200 {
				resultPreview = resultPreview[:200] + "..."
			}
			summary += fmt.Sprintf("✅ %s: %s\n", r.Label, resultPreview)
		}
	}

	return &ToolResult{
		ForLLM:  summary,
		ForUser: summary,
		Silent:  false,
		IsError: false,
		Async:   false,
	}
}
