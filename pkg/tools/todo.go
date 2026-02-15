package tools

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TodoItem 表示单个待办事项
type TodoItem struct {
	ID          string     `json:"id"`
	Content     string     `json:"content"`
	Status      string     `json:"status"`   // pending, in_progress, done, cancelled
	Priority    string     `json:"priority"` // low, medium, high
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TodoManager 管理所有待办事项
type TodoManager struct {
	todos map[string]*TodoItem // chatID -> todo list (simplified as session-based)
	mu    sync.RWMutex
}

var (
	todoManagerInstance *TodoManager
	todoManagerOnce     sync.Once
)

// GetTodoManager 获取 TodoManager 单例
func GetTodoManager() *TodoManager {
	todoManagerOnce.Do(func() {
		todoManagerInstance = &TodoManager{
			todos: make(map[string]*TodoItem),
		}
	})
	return todoManagerInstance
}

// CreateTodo 创建新的待办事项
func (m *TodoManager) CreateTodo(chatID, content, priority string) *TodoItem {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := fmt.Sprintf("todo_%d", time.Now().UnixNano())

	if priority == "" {
		priority = "medium"
	}

	todo := &TodoItem{
		ID:        id,
		Content:   content,
		Status:    "pending",
		Priority:  priority,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// 使用 chatID + id 作为唯一键
	key := fmt.Sprintf("%s:%s", chatID, id)
	m.todos[key] = todo

	return todo
}

// GetTodo 获取待办事项
func (m *TodoManager) GetTodo(chatID, id string) (*TodoItem, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s:%s", chatID, id)
	todo, ok := m.todos[key]
	return todo, ok
}

// UpdateTodo 更新待办事项
func (m *TodoManager) UpdateTodo(chatID, id, status string) (*TodoItem, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s", chatID, id)
	todo, ok := m.todos[key]
	if !ok {
		return nil, false
	}

	todo.Status = status
	todo.UpdatedAt = time.Now()

	if status == "done" {
		now := time.Now()
		todo.CompletedAt = &now
	}

	return todo, true
}

// DeleteTodo 删除待办事项
func (m *TodoManager) DeleteTodo(chatID, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s", chatID, id)
	_, ok := m.todos[key]
	if ok {
		delete(m.todos, key)
		return true
	}
	return false
}

// ListTodos 列出所有待办事项
func (m *TodoManager) ListTodos(chatID string, status string) []*TodoItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*TodoItem
	prefix := chatID + ":"

	for key, todo := range m.todos {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			if status == "" || todo.Status == status {
				result = append(result, todo)
			}
		}
	}

	return result
}

// ClearTodos 清除所有待办事项
func (m *TodoManager) ClearTodos(chatID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	prefix := chatID + ":"

	for key := range m.todos {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(m.todos, key)
			count++
		}
	}

	return count
}

// TodoTool 待办事项管理工具
type TodoTool struct {
	channel string
	chatID  string
}

func NewTodoTool() *TodoTool {
	return &TodoTool{}
}

func (t *TodoTool) Name() string {
	return "todo"
}

func (t *TodoTool) Description() string {
	return "Manage todo items for task tracking. Supports creating, updating, listing, and deleting todos."
}

func (t *TodoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"create", "update", "delete", "list", "clear"},
				"description": "The action to perform on todos",
			},
			"id": map[string]interface{}{
				"type":        "string",
				"description": "The todo ID (required for update/delete)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "The todo content (required for create)",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"pending", "in_progress", "done", "cancelled"},
				"description": "The todo status (for create/update)",
			},
			"priority": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"low", "medium", "high"},
				"description": "The todo priority (for create)",
			},
			"filter": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"", "pending", "in_progress", "done", "cancelled"},
				"description": "Filter by status (for list)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *TodoTool) SetContext(channel, chatID string) {
	t.channel = channel
	t.chatID = chatID
}

func (t *TodoTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	manager := GetTodoManager()

	// 使用 channel:chatID 作为上下文标识
	contextID := fmt.Sprintf("%s:%s", t.channel, t.chatID)

	switch action {
	case "create":
		content, _ := args["content"].(string)
		priority, _ := args["priority"].(string)

		if content == "" {
			return ErrorResult("content is required for create action")
		}

		todo := manager.CreateTodo(contextID, content, priority)
		return SuccessResult(fmt.Sprintf("✅ Created todo: %s\nID: %s\nPriority: %s\nStatus: %s",
			todo.Content, todo.ID, todo.Priority, todo.Status)).
			WithForUser(fmt.Sprintf("✅ 已创建待办事项：%s\n🆔 ID: %s\n🔔 优先级: %s\n📋 状态: %s",
				todo.Content, todo.ID, formatPriority(todo.Priority), formatStatus(todo.Status)))

	case "update":
		id, _ := args["id"].(string)
		status, _ := args["status"].(string)

		if id == "" {
			return ErrorResult("id is required for update action")
		}
		if status == "" {
			return ErrorResult("status is required for update action")
		}

		todo, ok := manager.UpdateTodo(contextID, id, status)
		if !ok {
			return ErrorResult(fmt.Sprintf("todo with id %s not found", id))
		}

		return SuccessResult(fmt.Sprintf("✅ Updated todo %s to status: %s", todo.ID, todo.Status)).
			WithForUser(fmt.Sprintf("✅ 已更新待办事项 %s\n📋 新状态: %s",
				todo.ID, formatStatus(todo.Status)))

	case "delete":
		id, _ := args["id"].(string)

		if id == "" {
			return ErrorResult("id is required for delete action")
		}

		if ok := manager.DeleteTodo(contextID, id); !ok {
			return ErrorResult(fmt.Sprintf("todo with id %s not found", id))
		}

		return SuccessResult(fmt.Sprintf("✅ Deleted todo %s", id)).
			WithForUser(fmt.Sprintf("🗑️ 已删除待办事项 %s", id))

	case "list":
		filter, _ := args["filter"].(string)
		todos := manager.ListTodos(contextID, filter)

		if len(todos) == 0 {
			return SuccessResult("No todos found").
				WithForUser("📝 暂无待办事项")
		}

		result := formatTodoList(todos)
		return SuccessResult(result).WithForUser(result)

	case "clear":
		count := manager.ClearTodos(contextID)
		return SuccessResult(fmt.Sprintf("✅ Cleared %d todos", count)).
			WithForUser(fmt.Sprintf("🧹 已清除 %d 个待办事项", count))

	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func formatPriority(priority string) string {
	switch priority {
	case "high":
		return "🔴 高"
	case "medium":
		return "🟡 中"
	case "low":
		return "🟢 低"
	default:
		return priority
	}
}

func formatStatus(status string) string {
	switch status {
	case "pending":
		return "⏳ 待处理"
	case "in_progress":
		return "🔄 进行中"
	case "done":
		return "✅ 已完成"
	case "cancelled":
		return "❌ 已取消"
	default:
		return status
	}
}

func formatTodoList(todos []*TodoItem) string {
	var result string
	result = "📋 待办事项列表\n"
	result += "═══════════════════\n\n"

	// 按状态分组
	groups := map[string][]*TodoItem{
		"pending":     {},
		"in_progress": {},
		"done":        {},
		"cancelled":   {},
	}

	for _, todo := range todos {
		groups[todo.Status] = append(groups[todo.Status], todo)
	}

	// 按优先级排序显示
	statusOrder := []string{"in_progress", "pending", "done", "cancelled"}
	statusLabels := map[string]string{
		"in_progress": "🔄 进行中",
		"pending":     "⏳ 待处理",
		"done":        "✅ 已完成",
		"cancelled":   "❌ 已取消",
	}

	for _, status := range statusOrder {
		items := groups[status]
		if len(items) == 0 {
			continue
		}

		result += statusLabels[status] + "\n"

		// 按优先级排序
		priorityOrder := []string{"high", "medium", "low"}
		for _, priority := range priorityOrder {
			for _, todo := range items {
				if todo.Priority == priority {
					result += fmt.Sprintf("  %s %s\n     🆔 %s\n",
						formatPriority(todo.Priority),
						todo.Content,
						todo.ID)
				}
			}
		}
		result += "\n"
	}

	return result
}
