package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// AgentRole 定义 Agent 的角色类型
type AgentRole string

const (
	// RoleGeneral 通用助手 - 默认角色，适合日常对话
	RoleGeneral AgentRole = "general"
	// RoleBuild 构建专家 - 专注于代码编辑和构建
	RoleBuild AgentRole = "build"
	// RolePlan 规划专家 - 专注于计划和架构设计
	RolePlan AgentRole = "plan"
	// RoleExplore 探索专家 - 专注于探索和调研
	RoleExplore AgentRole = "explore"
	// RoleDebug 调试专家 - 专注于问题诊断和调试
	RoleDebug AgentRole = "debug"
	// RoleReview 代码审查 - 专注于代码审查和优化建议
	RoleReview AgentRole = "review"
	// RoleDoc 文档专家 - 专注于文档编写
	RoleDoc AgentRole = "doc"
)

// AgentConfig 定义单个 Agent 的配置
type AgentConfig struct {
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	Role             AgentRole              `json:"role"`
	SystemPrompt     string                 `json:"system_prompt"`
	Model            string                 `json:"model"`
	Temperature      float64                `json:"temperature"`
	MaxTokens        int                    `json:"max_tokens"`
	MaxIterations    int                    `json:"max_iterations"`
	AllowedTools     []string               `json:"allowed_tools"`     // 允许使用的工具列表，空表示全部
	DeniedTools      []string               `json:"denied_tools"`      // 禁止使用的工具列表
	RequiredSkills   []string               `json:"required_skills"`   // 必须加载的技能
	CustomParameters map[string]interface{} `json:"custom_parameters"` // 自定义参数
}

// AgentRegistry 管理所有 Agent 配置
type AgentRegistry struct {
	agents       map[string]*AgentConfig
	defaultAgent string
	mu           sync.RWMutex
}

// NewAgentRegistry 创建新的 Agent 注册表
func NewAgentRegistry() *AgentRegistry {
	registry := &AgentRegistry{
		agents:       make(map[string]*AgentConfig),
		defaultAgent: "general",
	}

	// 注册默认 Agent
	registry.registerDefaultAgents()

	return registry
}

// registerDefaultAgents 注册内置的默认 Agent
func (r *AgentRegistry) registerDefaultAgents() {
	defaults := []*AgentConfig{
		{
			Name:        "general",
			Description: "General-purpose assistant for everyday tasks and conversations",
			Role:        RoleGeneral,
			SystemPrompt: `You are a helpful AI assistant. You can help users with various tasks including:
- Answering questions
- Providing information and explanations
- Helping with writing and editing
- Assisting with problem-solving
- Engaging in friendly conversation

Always be helpful, accurate, and concise. If you're unsure about something, say so.`,
			Temperature:   0.7,
			MaxTokens:     8192,
			MaxIterations: 20,
			AllowedTools:  []string{}, // 空表示允许所有
		},
		{
			Name:        "build",
			Description: "Expert in code editing, building, and software development",
			Role:        RoleBuild,
			SystemPrompt: `You are an expert software developer and build engineer. You excel at:
- Reading and understanding code
- Writing clean, efficient code
- Refactoring and improving existing code
- Debugging and fixing bugs
- Building and testing software
- Following best practices and design patterns

When working with code:
1. Always read the relevant files first
2. Understand the existing patterns and conventions
3. Make minimal, focused changes
4. Verify your changes don't break anything
5. Follow the project's style guidelines

Be precise and careful with code edits.`,
			Temperature:   0.3,
			MaxTokens:     8192,
			MaxIterations: 30,
			AllowedTools: []string{
				"read_file", "write_file", "edit_file", "append_file",
				"list_dir", "exec", "glob", "grep",
				"web_search", "web_fetch", "message",
			},
		},
		{
			Name:        "plan",
			Description: "Expert in planning, architecture design, and project organization",
			Role:        RolePlan,
			SystemPrompt: `You are an expert in software architecture and project planning. You excel at:
- Analyzing requirements and constraints
- Designing system architecture
- Creating implementation plans
- Breaking down complex tasks
- Identifying risks and dependencies
- Suggesting best approaches and technologies

When planning:
1. Gather all relevant information first
2. Consider multiple approaches
3. Analyze trade-offs
4. Create clear, actionable steps
5. Identify potential risks
6. Provide clear rationale for your recommendations

Focus on clarity and practicality.`,
			Temperature:   0.5,
			MaxTokens:     8192,
			MaxIterations: 15,
			AllowedTools: []string{
				"read_file", "list_dir", "glob",
				"web_search", "web_fetch", "message", "todo",
			},
		},
		{
			Name:        "explore",
			Description: "Expert in exploration, research, and investigation",
			Role:        RoleExplore,
			SystemPrompt: `You are an expert researcher and explorer. You excel at:
- Investigating codebases and systems
- Researching technologies and solutions
- Finding relevant information
- Understanding unfamiliar domains
- Discovering patterns and insights

When exploring:
1. Start with high-level understanding
2. Dive deep into relevant areas
3. Take notes and organize findings
4. Ask questions when needed
5. Synthesize information clearly

Be thorough but efficient in your exploration.`,
			Temperature:   0.6,
			MaxTokens:     8192,
			MaxIterations: 25,
			AllowedTools: []string{
				"read_file", "list_dir", "glob", "grep",
				"web_search", "web_fetch", "code_search",
				"message", "todo",
			},
		},
		{
			Name:        "debug",
			Description: "Expert in debugging, troubleshooting, and problem diagnosis",
			Role:        RoleDebug,
			SystemPrompt: `You are an expert debugger and troubleshooter. You excel at:
- Analyzing error messages and logs
- Identifying root causes
- Reproducing issues
- Finding and fixing bugs
- Optimizing performance
- Understanding complex systems

When debugging:
1. Gather all error information
2. Read relevant code carefully
3. Form hypotheses about the cause
4. Test your hypotheses systematically
5. Verify the fix thoroughly
6. Consider edge cases

Be methodical and precise in your analysis.`,
			Temperature:   0.2,
			MaxTokens:     8192,
			MaxIterations: 30,
			AllowedTools: []string{
				"read_file", "list_dir", "glob", "grep",
				"exec", "web_search", "web_fetch",
				"message", "todo",
			},
		},
		{
			Name:        "review",
			Description: "Expert in code review and quality assurance",
			Role:        RoleReview,
			SystemPrompt: `You are an expert code reviewer. You excel at:
- Reviewing code for quality and correctness
- Identifying potential bugs and issues
- Suggesting improvements and optimizations
- Ensuring adherence to best practices
- Checking security and performance
- Providing constructive feedback

When reviewing code:
1. Understand the context and requirements
2. Check for correctness and edge cases
3. Look for code smells and anti-patterns
4. Verify error handling
5. Check for security issues
6. Suggest specific improvements

Be thorough, constructive, and educational.`,
			Temperature:   0.4,
			MaxTokens:     8192,
			MaxIterations: 20,
			AllowedTools: []string{
				"read_file", "list_dir", "glob", "grep",
				"web_search", "message",
			},
		},
		{
			Name:        "doc",
			Description: "Expert in technical writing and documentation",
			Role:        RoleDoc,
			SystemPrompt: `You are an expert technical writer. You excel at:
- Writing clear documentation
- Creating README files
- Documenting APIs and interfaces
- Writing tutorials and guides
- Improving existing documentation
- Organizing information effectively

When writing documentation:
1. Understand the audience and purpose
2. Be clear, concise, and accurate
3. Use examples where helpful
4. Structure information logically
5. Include necessary context
6. Follow documentation best practices

Focus on clarity and usefulness.`,
			Temperature:   0.5,
			MaxTokens:     8192,
			MaxIterations: 15,
			AllowedTools: []string{
				"read_file", "write_file", "edit_file",
				"list_dir", "glob", "message",
			},
		},
	}

	for _, agent := range defaults {
		r.agents[agent.Name] = agent
	}
}

// GetAgent 获取指定名称的 Agent 配置
func (r *AgentRegistry) GetAgent(name string) (*AgentConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[name]
	if !ok {
		return nil, false
	}

	// 返回副本以避免外部修改
	copy := *agent
	return &copy, true
}

// GetDefaultAgent 获取默认 Agent
func (r *AgentRegistry) GetDefaultAgent() (*AgentConfig, bool) {
	return r.GetAgent(r.defaultAgent)
}

// SetDefaultAgent 设置默认 Agent
func (r *AgentRegistry) SetDefaultAgent(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[name]; !ok {
		return fmt.Errorf("agent %s not found", name)
	}

	r.defaultAgent = name
	return nil
}

// RegisterAgent 注册新的 Agent
func (r *AgentRegistry) RegisterAgent(agent *AgentConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent.Name == "" {
		return fmt.Errorf("agent name is required")
	}

	r.agents[agent.Name] = agent
	return nil
}

// ListAgents 列出所有可用的 Agent
func (r *AgentRegistry) ListAgents() []AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var list []AgentInfo
	for name, agent := range r.agents {
		list = append(list, AgentInfo{
			Name:        name,
			Description: agent.Description,
			Role:        string(agent.Role),
			IsDefault:   name == r.defaultAgent,
		})
	}

	return list
}

// AgentInfo 包含 Agent 的基本信息
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Role        string `json:"role"`
	IsDefault   bool   `json:"is_default"`
}

// IsToolAllowed 检查工具是否被允许使用
func (c *AgentConfig) IsToolAllowed(toolName string) bool {
	// 如果在禁止列表中，不允许使用
	for _, denied := range c.DeniedTools {
		if denied == toolName {
			return false
		}
	}

	// 如果允许列表为空，允许所有工具
	if len(c.AllowedTools) == 0 {
		return true
	}

	// 检查是否在允许列表中
	for _, allowed := range c.AllowedTools {
		if allowed == toolName {
			return true
		}
	}

	return false
}

// LoadCustomAgents 从目录加载自定义 Agent 配置
func (r *AgentRegistry) LoadCustomAgents(agentsDir string) error {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 目录不存在，不报错
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// 只处理 .json 文件
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		path := filepath.Join(agentsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue // 读取失败，跳过
		}

		var agent AgentConfig
		if err := json.Unmarshal(data, &agent); err != nil {
			continue // 解析失败，跳过
		}

		// 设置默认名称（从文件名）
		if agent.Name == "" {
			agent.Name = entry.Name()[:len(entry.Name())-5] // 去掉 .json
		}

		r.RegisterAgent(&agent)
	}

	return nil
}

// SaveAgentConfig 保存 Agent 配置到文件
func SaveAgentConfig(agent *AgentConfig, path string) error {
	data, err := json.MarshalIndent(agent, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
