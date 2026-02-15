package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Kibidango086/mortis-agent/pkg/utils"
)

// GlobTool 文件模式匹配工具
type GlobTool struct {
	workspace string
	restrict  bool
}

func NewGlobTool(workspace string, restrict bool) *GlobTool {
	return &GlobTool{
		workspace: workspace,
		restrict:  restrict,
	}
}

func (t *GlobTool) Name() string {
	return "glob"
}

func (t *GlobTool) Description() string {
	return `Find files by glob pattern matching. Supports wildcards like * and **.
Returns a list of file paths matching the pattern.
Use this when you need to find files by name patterns.`
}

func (t *GlobTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "The glob pattern to match (e.g., '*.go', '**/*.md', 'src/**/*.js')",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "The directory to search in (default: workspace root)",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum number of results to return (default: 100)",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GlobTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	pattern, _ := args["pattern"].(string)
	path, _ := args["path"].(string)
	limit := 100
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	if pattern == "" {
		return ErrorResult("pattern is required")
	}

	// 确定搜索路径
	searchPath := t.workspace
	if path != "" {
		searchPath = filepath.Join(t.workspace, path)
	}

	// 安全检查
	if t.restrict {
		if !utils.IsPathWithinWorkspace(searchPath, t.workspace) {
			return ErrorResult("path is outside of workspace")
		}
	}

	// 执行 glob 匹配
	matches, err := t.glob(searchPath, pattern, limit)
	if err != nil {
		return ErrorResult(fmt.Sprintf("glob failed: %v", err))
	}

	if len(matches) == 0 {
		return SuccessResult("No files found matching the pattern").
			WithForUser(fmt.Sprintf("🔍 未找到匹配 '%s' 的文件", pattern))
	}

	// 格式化结果
	result := t.formatGlobResults(matches, pattern)
	return SuccessResult(result).WithForUser(result)
}

func (t *GlobTool) glob(root, pattern string, limit int) ([]string, error) {
	var matches []string

	// 转换为绝对路径
	if !filepath.IsAbs(root) {
		var err error
		root, err = filepath.Abs(root)
		if err != nil {
			return nil, err
		}
	}

	// 检查是否是双星号模式（递归匹配）
	if strings.Contains(pattern, "**") {
		// 处理 ** 模式
		pattern = strings.ReplaceAll(pattern, "**/*", "*")
		pattern = strings.ReplaceAll(pattern, "**", "*")

		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // 继续遍历
			}

			if len(matches) >= limit {
				return filepath.SkipDir
			}

			if info.IsDir() {
				return nil
			}

			// 获取相对路径用于匹配
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}

			// 使用 filepath.Match 进行模式匹配
			matched, err := filepath.Match(pattern, rel)
			if err != nil {
				return nil
			}

			if matched || t.matchPattern(filepath.Base(path), pattern) {
				// 返回相对路径
				relPath, _ := filepath.Rel(t.workspace, path)
				matches = append(matches, relPath)
			}

			return nil
		})

		if err != nil {
			return nil, err
		}
	} else {
		// 单层匹配
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			if len(matches) >= limit {
				break
			}

			name := entry.Name()
			matched, err := filepath.Match(pattern, name)
			if err != nil {
				continue
			}

			if matched {
				relPath, _ := filepath.Rel(t.workspace, filepath.Join(root, name))
				matches = append(matches, relPath)
			}
		}
	}

	// 排序结果
	sort.Strings(matches)

	return matches, nil
}

func (t *GlobTool) matchPattern(name, pattern string) bool {
	matched, _ := filepath.Match(pattern, name)
	return matched
}

func (t *GlobTool) formatGlobResults(matches []string, pattern string) string {
	var result string
	result = fmt.Sprintf("📁 Glob Results: '%s'\n", pattern)
	result += "═══════════════════\n\n"

	// 按目录分组
	groups := make(map[string][]string)
	for _, match := range matches {
		dir := filepath.Dir(match)
		if dir == "." {
			dir = "(root)"
		}
		groups[dir] = append(groups[dir], filepath.Base(match))
	}

	// 按目录排序
	var dirs []string
	for dir := range groups {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		result += fmt.Sprintf("📂 %s/\n", dir)
		files := groups[dir]
		sort.Strings(files)
		for _, file := range files {
			result += fmt.Sprintf("   📄 %s\n", file)
		}
		result += "\n"
	}

	result += fmt.Sprintf("📊 Found %d files\n", len(matches))

	return result
}

// GrepTool 内容搜索工具
type GrepTool struct {
	workspace string
	restrict  bool
}

func NewGrepTool(workspace string, restrict bool) *GrepTool {
	return &GrepTool{
		workspace: workspace,
		restrict:  restrict,
	}
}

func (t *GrepTool) Name() string {
	return "grep"
}

func (t *GrepTool) Description() string {
	return `Search file contents using pattern matching.
Supports regular expressions and can search across multiple files.
Use this when you need to find specific text in files.`
}

func (t *GrepTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "The search pattern (regex supported)",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "The directory or file to search in (default: workspace root)",
			},
			"include": map[string]interface{}{
				"type":        "string",
				"description": "File pattern to include (e.g., '*.go', '*.js')",
			},
			"exclude": map[string]interface{}{
				"type":        "string",
				"description": "File pattern to exclude (e.g., '*.test.go')",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum number of results to return (default: 50)",
			},
			"case_sensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether the search is case sensitive (default: false)",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GrepTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	pattern, _ := args["pattern"].(string)
	path, _ := args["path"].(string)
	include, _ := args["include"].(string)
	exclude, _ := args["exclude"].(string)
	limit := 50
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	caseSensitive := false
	if cs, ok := args["case_sensitive"].(bool); ok {
		caseSensitive = cs
	}

	if pattern == "" {
		return ErrorResult("pattern is required")
	}

	// 确定搜索路径
	searchPath := t.workspace
	if path != "" {
		searchPath = filepath.Join(t.workspace, path)
	}

	// 安全检查
	if t.restrict {
		if !utils.IsPathWithinWorkspace(searchPath, t.workspace) {
			return ErrorResult("path is outside of workspace")
		}
	}

	// 执行搜索
	results, err := t.grep(searchPath, pattern, include, exclude, limit, caseSensitive)
	if err != nil {
		return ErrorResult(fmt.Sprintf("grep failed: %v", err))
	}

	if len(results) == 0 {
		return SuccessResult("No matches found").
			WithForUser(fmt.Sprintf("🔍 未找到匹配 '%s' 的内容", pattern))
	}

	// 格式化结果
	result := t.formatGrepResults(results, pattern)
	return SuccessResult(result).WithForUser(result)
}

type GrepResult struct {
	File    string
	Line    int
	Column  int
	Content string
	Match   string
}

func (t *GrepTool) grep(root, pattern, include, exclude string, limit int, caseSensitive bool) ([]GrepResult, error) {
	var results []GrepResult

	// 准备模式
	if !caseSensitive {
		pattern = strings.ToLower(pattern)
	}

	// 遍历目录
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if len(results) >= limit {
			return filepath.SkipDir
		}

		if info.IsDir() {
			return nil
		}

		// 跳过二进制文件和大文件
		if info.Size() > 1024*1024 { // 1MB
			return nil
		}

		// 检查 include 模式
		if include != "" {
			matched, _ := filepath.Match(include, filepath.Base(path))
			if !matched {
				return nil
			}
		}

		// 检查 exclude 模式
		if exclude != "" {
			matched, _ := filepath.Match(exclude, filepath.Base(path))
			if matched {
				return nil
			}
		}

		// 读取文件内容
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		// 检查是否是二进制文件
		if isBinary(content) {
			return nil
		}

		// 搜索内容
		lines := strings.Split(string(content), "\n")
		for lineNum, line := range lines {
			searchLine := line
			if !caseSensitive {
				searchLine = strings.ToLower(line)
			}

			if idx := strings.Index(searchLine, pattern); idx >= 0 {
				relPath, _ := filepath.Rel(t.workspace, path)

				// 提取匹配周围的上下文
				start := idx - 30
				if start < 0 {
					start = 0
				}
				end := idx + len(pattern) + 30
				if end > len(line) {
					end = len(line)
				}

				context := line[start:end]
				if start > 0 {
					context = "..." + context
				}
				if end < len(line) {
					context = context + "..."
				}

				results = append(results, GrepResult{
					File:    relPath,
					Line:    lineNum + 1,
					Column:  idx + 1,
					Content: context,
					Match:   line[idx : idx+len(pattern)],
				})

				if len(results) >= limit {
					return filepath.SkipDir
				}
			}
		}

		return nil
	})

	return results, err
}

func isBinary(content []byte) bool {
	// 简单的二进制检测：检查是否有空字节
	for i := 0; i < len(content) && i < 8000; i++ {
		if content[i] == 0 {
			return true
		}
	}
	return false
}

func (t *GrepTool) formatGrepResults(results []GrepResult, pattern string) string {
	var result string
	result = fmt.Sprintf("🔍 Grep Results: '%s'\n", pattern)
	result += "═══════════════════\n\n"

	// 按文件分组
	groups := make(map[string][]GrepResult)
	for _, r := range results {
		groups[r.File] = append(groups[r.File], r)
	}

	// 按文件排序
	var files []string
	for file := range groups {
		files = append(files, file)
	}
	sort.Strings(files)

	for _, file := range files {
		result += fmt.Sprintf("📄 %s\n", file)
		matches := groups[file]
		for _, match := range matches {
			result += fmt.Sprintf("   %d:%d  %s\n", match.Line, match.Column, match.Content)
		}
		result += "\n"
	}

	result += fmt.Sprintf("📊 Found %d matches in %d files\n", len(results), len(groups))

	return result
}
