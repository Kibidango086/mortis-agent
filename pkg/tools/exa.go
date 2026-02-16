package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ExaSearchProvider Exa AI 搜索提供器 - 使用 MCP API (opencode 风格)
type ExaSearchProvider struct {
	apiKey string
}

func NewExaSearchProvider(apiKey string) *ExaSearchProvider {
	return &ExaSearchProvider{apiKey: apiKey}
}

// McpSearchRequest MCP 搜索请求结构
type McpSearchRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  struct {
		Name      string `json:"name"`
		Arguments struct {
			Query                string `json:"query"`
			NumResults           int    `json:"numResults,omitempty"`
			Livecrawl            string `json:"livecrawl,omitempty"`
			Type                 string `json:"type,omitempty"`
			ContextMaxCharacters int    `json:"contextMaxCharacters,omitempty"`
		} `json:"arguments"`
	} `json:"params"`
}

// McpSearchResponse MCP 搜索响应结构
type McpSearchResponse struct {
	JSONRPC string `json:"jsonrpc"`
	Result  struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

// Search performs web search using Exa AI MCP API
func (p *ExaSearchProvider) Search(ctx context.Context, query string, opts SearchOptions) (string, error) {
	if p.apiKey == "" {
		return "", fmt.Errorf("Exa API key is not configured")
	}

	// Set defaults
	if opts.NumResults <= 0 {
		opts.NumResults = 8
	}
	if opts.Livecrawl == "" {
		opts.Livecrawl = "fallback"
	}
	if opts.Type == "" {
		opts.Type = "auto"
	}

	searchRequest := McpSearchRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	searchRequest.Params.Name = "web_search_exa"
	searchRequest.Params.Arguments.Query = query
	searchRequest.Params.Arguments.NumResults = opts.NumResults
	searchRequest.Params.Arguments.Livecrawl = opts.Livecrawl
	searchRequest.Params.Arguments.Type = opts.Type
	if opts.ContextMaxCharacters > 0 {
		searchRequest.Params.Arguments.ContextMaxCharacters = opts.ContextMaxCharacters
	}

	requestBody, err := json.Marshal(searchRequest)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://mcp.exa.ai/mcp", bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("search error (%d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Parse SSE response
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			var data McpSearchResponse
			if err := json.Unmarshal([]byte(line[6:]), &data); err != nil {
				continue
			}
			if len(data.Result.Content) > 0 {
				return data.Result.Content[0].Text, nil
			}
		}
	}

	return "No search results found. Please try a different query.", nil
}

// ExaCodeSearchProvider Exa AI 代码搜索提供器
type ExaCodeSearchProvider struct {
	apiKey string
}

func NewExaCodeSearchProvider(apiKey string) *ExaCodeSearchProvider {
	return &ExaCodeSearchProvider{apiKey: apiKey}
}

// ExaCodeResult Exa 代码搜索结果
type ExaCodeResult struct {
	Title    string  `json:"title"`
	URL      string  `json:"url"`
	Author   string  `json:"author,omitempty"`
	Repo     string  `json:"repo,omitempty"`
	Language string  `json:"language,omitempty"`
	Score    float64 `json:"score"`
}

// ExaCodeSearchResponse Exa 代码搜索响应
type ExaCodeSearchResponse struct {
	Results []ExaCodeResult `json:"results"`
}

// ExaSearchResult Exa 搜索结果 (旧 API)
type ExaSearchResult struct {
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	Author        string  `json:"author,omitempty"`
	PublishedDate string  `json:"publishedDate,omitempty"`
	Score         float64 `json:"score"`
}

// ExaSearchResponse Exa 搜索响应 (旧 API)
type ExaSearchResponse struct {
	Results []ExaSearchResult `json:"results"`
}

func (p *ExaCodeSearchProvider) Search(ctx context.Context, query string, language string, count int) (string, error) {
	if p.apiKey == "" {
		return "", fmt.Errorf("Exa API key is not configured")
	}

	url := "https://api.exa.ai/search"

	requestBody := map[string]interface{}{
		"query":         query,
		"numResults":    count,
		"useAutoprompt": true,
		"type":          "neural",
		"contents": map[string]interface{}{
			"text": true,
		},
	}

	// 如果指定了语言，添加到查询中
	if language != "" {
		requestBody["query"] = fmt.Sprintf("%s language:%s", query, language)
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Exa API error (status %d): %s", resp.StatusCode, string(body))
	}

	var searchResp ExaCodeSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		// 尝试解析为通用格式
		var genericResp ExaSearchResponse
		if err := json.Unmarshal(body, &genericResp); err != nil {
			return "", fmt.Errorf("failed to parse response: %w", err)
		}
		// 转换为代码结果格式
		for _, r := range genericResp.Results {
			searchResp.Results = append(searchResp.Results, ExaCodeResult{
				Title: r.Title,
				URL:   r.URL,
				Score: r.Score,
			})
		}
	}

	if len(searchResp.Results) == 0 {
		return fmt.Sprintf("No code results found for: %s", query), nil
	}

	return p.formatResults(searchResp.Results, query, language), nil
}

func (p *ExaCodeSearchProvider) formatResults(results []ExaCodeResult, query, language string) string {
	var output string
	output = fmt.Sprintf("💻 Code Search Results: '%s'", query)
	if language != "" {
		output += fmt.Sprintf(" (language: %s)", language)
	}
	output += "\n═══════════════════\n\n"

	for i, result := range results {
		output += fmt.Sprintf("%d. **%s**\n", i+1, result.Title)
		output += fmt.Sprintf("   🔗 %s\n", result.URL)
		if result.Author != "" {
			output += fmt.Sprintf("   👤 %s\n", result.Author)
		}
		if result.Repo != "" {
			output += fmt.Sprintf("   📦 %s\n", result.Repo)
		}
		if result.Language != "" {
			output += fmt.Sprintf("   📝 %s\n", result.Language)
		}
		output += fmt.Sprintf("   ⭐ Relevance: %.2f\n", result.Score)
		output += "\n"
	}

	output += fmt.Sprintf("📊 Found %d code results\n", len(results))
	return output
}
