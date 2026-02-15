package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ChatWithStreamBase 是 ChatWithStream 的基础实现
// 对于不支持原生流式的 provider，它会模拟流式行为
func ChatWithStreamBase(
	ctx context.Context,
	chatFn func(context.Context, []Message, []ToolDefinition, string, map[string]interface{}) (*LLMResponse, error),
	messages []Message,
	tools []ToolDefinition,
	model string,
	opts ChatOptions,
) (*LLMResponse, error) {
	// 如果提供了工具调用回调，立即调用（模拟流式工具调用通知）
	if opts.ToolCallCallback != nil && len(tools) > 0 {
		// 这里我们无法预知工具调用，所以跳过
	}

	// 调用底层的 Chat 方法
	options := map[string]interface{}{
		"max_tokens":  opts.MaxTokens,
		"temperature": opts.Temperature,
	}

	resp, err := chatFn(ctx, messages, tools, model, options)
	if err != nil {
		return nil, err
	}

	// 如果有工具调用，触发回调
	if opts.ToolCallCallback != nil {
		for _, tc := range resp.ToolCalls {
			opts.ToolCallCallback(tc.Name, tc.Arguments)
		}
	}

	// 如果有流式回调，模拟流式内容输出
	if opts.StreamCallback != nil && resp.Content != "" {
		// 将内容分成小块发送，模拟流式效果
		content := resp.Content
		chunkSize := 10 // 每次发送10个字符

		for i := 0; i < len(content); i += chunkSize {
			end := i + chunkSize
			if end > len(content) {
				end = len(content)
			}
			chunk := content[i:end]
			opts.StreamCallback(chunk, false)
		}

		// 标记完成
		opts.StreamCallback("", true)
	}

	return resp, nil
}

// MockStreamResponse 创建一个模拟的流式响应
// 用于测试或演示
func MockStreamResponse(content string, opts ChatOptions) *LLMResponse {
	// 触发流式回调
	if opts.StreamCallback != nil && content != "" {
		// 按句子分割
		sentences := splitIntoSentences(content)
		for _, sentence := range sentences {
			opts.StreamCallback(sentence, false)
		}
		opts.StreamCallback("", true)
	}

	return &LLMResponse{
		Content:      content,
		FinishReason: "stop",
	}
}

// splitIntoSentences 将文本分割成句子
func splitIntoSentences(text string) []string {
	// 简单的句子分割，基于标点符号
	var sentences []string
	var current strings.Builder

	for i, r := range text {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' || r == '\n' {
			// 检查是否是缩写（如 Mr. Dr. 等）
			if i+1 < len(text) {
				nextChar := text[i+1]
				if nextChar == ' ' || nextChar == '\n' {
					sentences = append(sentences, current.String())
					current.Reset()
				}
			} else {
				sentences = append(sentences, current.String())
				current.Reset()
			}
		}
	}

	// 添加剩余内容
	if current.Len() > 0 {
		sentences = append(sentences, current.String())
	}

	// 如果没有分割（没有标点符号），返回整个文本
	if len(sentences) == 0 && text != "" {
		sentences = append(sentences, text)
	}

	return sentences
}

// FormatToolCallForDisplay 格式化工具调用用于显示
func FormatToolCallForDisplay(toolName string, args map[string]interface{}) string {
	argsJSON, _ := json.Marshal(args)
	argsStr := string(argsJSON)
	if len(argsStr) > 100 {
		argsStr = argsStr[:100] + "..."
	}
	return fmt.Sprintf("🔧 %s(%s)", toolName, argsStr)
}

// FormatToolResultForDisplay 格式化工具结果用于显示
func FormatToolResultForDisplay(toolName string, result string) string {
	preview := result
	if len(preview) > 100 {
		preview = preview[:100] + "..."
	}
	return fmt.Sprintf("✅ %s: %s", toolName, preview)
}
