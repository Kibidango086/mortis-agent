package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Kibidango086/mortis-agent/pkg/bus"
)

// Question 表示向用户提出的问题
type Question struct {
	ID         string     `json:"id"`
	ChatID     string     `json:"chat_id"`
	Channel    string     `json:"channel"`
	Content    string     `json:"content"`
	Options    []string   `json:"options,omitempty"`
	Answer     string     `json:"answer,omitempty"`
	Status     string     `json:"status"` // pending, answered, cancelled
	CreatedAt  time.Time  `json:"created_at"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
}

// QuestionManager 管理所有待回答问题
type QuestionManager struct {
	questions map[string]*Question
	callbacks map[string]chan string // question ID -> answer channel
	mu        sync.RWMutex
}

var (
	questionManagerInstance *QuestionManager
	questionManagerOnce     sync.Once
)

// GetQuestionManager 获取 QuestionManager 单例
func GetQuestionManager() *QuestionManager {
	questionManagerOnce.Do(func() {
		questionManagerInstance = &QuestionManager{
			questions: make(map[string]*Question),
			callbacks: make(map[string]chan string),
		}
	})
	return questionManagerInstance
}

// CreateQuestion 创建新问题
func (m *QuestionManager) CreateQuestion(channel, chatID, content string, options []string) *Question {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := fmt.Sprintf("q_%d", time.Now().UnixNano())

	question := &Question{
		ID:        id,
		Channel:   channel,
		ChatID:    chatID,
		Content:   content,
		Options:   options,
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	m.questions[id] = question
	// 创建 answer channel
	m.callbacks[id] = make(chan string, 1)

	return question
}

// GetQuestion 获取问题
func (m *QuestionManager) GetQuestion(id string) (*Question, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	q, ok := m.questions[id]
	return q, ok
}

// AnswerQuestion 回答问题
func (m *QuestionManager) AnswerQuestion(id, answer string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	q, ok := m.questions[id]
	if !ok || q.Status != "pending" {
		return false
	}

	now := time.Now()
	q.Answer = answer
	q.Status = "answered"
	q.AnsweredAt = &now

	// 通知等待的 callback
	if ch, ok := m.callbacks[id]; ok {
		select {
		case ch <- answer:
		default:
		}
	}

	return true
}

// WaitForAnswer 等待用户回答（带超时）
func (m *QuestionManager) WaitForAnswer(id string, timeout time.Duration) (string, bool) {
	m.mu.RLock()
	ch, ok := m.callbacks[id]
	m.mu.RUnlock()

	if !ok {
		return "", false
	}

	select {
	case answer := <-ch:
		return answer, true
	case <-time.After(timeout):
		return "", false
	}
}

// CancelQuestion 取消问题
func (m *QuestionManager) CancelQuestion(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	q, ok := m.questions[id]
	if !ok || q.Status != "pending" {
		return false
	}

	q.Status = "cancelled"

	// 关闭 channel
	if ch, ok := m.callbacks[id]; ok {
		close(ch)
		delete(m.callbacks, id)
	}

	return true
}

// CleanupExpired 清理过期的问题（超过30分钟）
func (m *QuestionManager) CleanupExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()

	expiration := time.Now().Add(-30 * time.Minute)
	for id, q := range m.questions {
		if q.Status == "pending" && q.CreatedAt.Before(expiration) {
			q.Status = "cancelled"
			if ch, ok := m.callbacks[id]; ok {
				close(ch)
				delete(m.callbacks, id)
			}
		}
	}
}

// QuestionTool 向用户提问的工具
type QuestionTool struct {
	channel string
	chatID  string
	bus     *bus.MessageBus
	manager *QuestionManager
}

func NewQuestionTool(msgBus *bus.MessageBus) *QuestionTool {
	return &QuestionTool{
		bus:     msgBus,
		manager: GetQuestionManager(),
	}
}

func (t *QuestionTool) Name() string {
	return "question"
}

func (t *QuestionTool) Description() string {
	return `Ask the user a question and wait for their response. 
Use this when you need clarification, user input, or confirmation before proceeding.
The question will be displayed to the user and the tool will wait for their answer.
Supports optional predefined options for the user to choose from.`
}

func (t *QuestionTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"question": map[string]interface{}{
				"type":        "string",
				"description": "The question to ask the user",
			},
			"options": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional list of options for the user to choose from",
			},
			"timeout": map[string]interface{}{
				"type":        "number",
				"description": "Timeout in seconds (default: 300 = 5 minutes)",
			},
		},
		"required": []string{"question"},
	}
}

func (t *QuestionTool) SetContext(channel, chatID string) {
	t.channel = channel
	t.chatID = chatID
}

func (t *QuestionTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	content, _ := args["question"].(string)

	if content == "" {
		return ErrorResult("question content is required")
	}

	// 解析选项
	var options []string
	if opts, ok := args["options"].([]interface{}); ok {
		for _, opt := range opts {
			if str, ok := opt.(string); ok {
				options = append(options, str)
			}
		}
	}

	// 解析超时
	timeout := 300 // 默认5分钟
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	// 创建问题
	question := t.manager.CreateQuestion(t.channel, t.chatID, content, options)

	// 格式化问题消息
	msgContent := formatQuestionMessage(question)

	// 发送问题到用户
	if t.bus != nil {
		t.bus.PublishOutbound(bus.OutboundMessage{
			Channel: t.channel,
			ChatID:  t.chatID,
			Content: msgContent,
		})
	}

	// 等待用户回答
	answer, ok := t.manager.WaitForAnswer(question.ID, time.Duration(timeout)*time.Second)
	if !ok {
		// 超时
		t.manager.CancelQuestion(question.ID)
		return ErrorResult("timeout waiting for user response").
			WithForUser("⏰ 等待用户回复超时")
	}

	// 返回答案
	return SuccessResult(fmt.Sprintf("User answered: %s", answer)).
		WithForUser(fmt.Sprintf("✅ 已收到回答: %s", answer))
}

func formatQuestionMessage(q *Question) string {
	var msg string

	msg = "🤔 **我需要您的帮助**\n\n"
	msg += q.Content + "\n\n"

	if len(q.Options) > 0 {
		msg += "💡 **请选择其中一个选项：**\n"
		for i, opt := range q.Options {
			msg += fmt.Sprintf("%d. %s\n", i+1, opt)
		}
		msg += "\n"
	}

	msg += fmt.Sprintf("📝 请直接回复您的答案（问题 ID: %s）", q.ID)

	return msg
}

// ProcessQuestionAnswer 处理用户回答问题
// 这个函数应该在接收到用户消息时调用
func ProcessQuestionAnswer(channel, chatID, answer string, msgBus *bus.MessageBus) bool {
	manager := GetQuestionManager()

	// 查找该聊天中待回答的问题
	manager.mu.RLock()
	var pendingQuestion *Question
	for _, q := range manager.questions {
		if q.Channel == channel && q.ChatID == chatID && q.Status == "pending" {
			pendingQuestion = q
			break
		}
	}
	manager.mu.RUnlock()

	if pendingQuestion == nil {
		return false // 没有待回答的问题
	}

	// 检查答案是否匹配选项（如果有选项的话）
	if len(pendingQuestion.Options) > 0 {
		// 尝试解析为选项索引
		var selectedOption string
		for i, opt := range pendingQuestion.Options {
			// 匹配数字索引
			if answer == fmt.Sprintf("%d", i+1) {
				selectedOption = opt
				break
			}
			// 匹配选项文本
			if answer == opt {
				selectedOption = opt
				break
			}
		}

		if selectedOption != "" {
			manager.AnswerQuestion(pendingQuestion.ID, selectedOption)
		} else {
			// 使用原始回答
			manager.AnswerQuestion(pendingQuestion.ID, answer)
		}
	} else {
		manager.AnswerQuestion(pendingQuestion.ID, answer)
	}

	return true
}
