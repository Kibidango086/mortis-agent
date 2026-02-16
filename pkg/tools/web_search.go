package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// SearchProvider is the interface for search providers
type SearchProvider interface {
	Search(ctx context.Context, query string, opts SearchOptions) (string, error)
}

// SearchOptions contains search options
type SearchOptions struct {
	NumResults           int
	Livecrawl            string // "fallback" or "preferred"
	Type                 string // "auto", "fast", "deep"
	ContextMaxCharacters int
}

// BraveSearchProvider uses Brave Search API
type BraveSearchProvider struct {
	apiKey string
}

func (p *BraveSearchProvider) Search(ctx context.Context, query string, opts SearchOptions) (string, error) {
	if opts.NumResults <= 0 {
		opts.NumResults = 5
	}

	searchURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), opts.NumResults)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var searchResp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	results := searchResp.Web.Results
	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s", query))
	for i, item := range results {
		if i >= opts.NumResults {
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, item.Title, item.URL))
		if item.Description != "" {
			lines = append(lines, fmt.Sprintf("   %s", item.Description))
		}
	}

	return strings.Join(lines, "\n"), nil
}

// DuckDuckGoSearchProvider uses DuckDuckGo HTML search
type DuckDuckGoSearchProvider struct{}

func (p *DuckDuckGoSearchProvider) Search(ctx context.Context, query string, opts SearchOptions) (string, error) {
	if opts.NumResults <= 0 {
		opts.NumResults = 5
	}

	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	return p.extractResults(string(body), opts.NumResults, query)
}

func (p *DuckDuckGoSearchProvider) extractResults(html string, count int, query string) (string, error) {
	reLink := regexp.MustCompile(`<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>([\s\S]*?)</a>`)
	matches := reLink.FindAllStringSubmatch(html, count+5)

	if len(matches) == 0 {
		return fmt.Sprintf("No results found or extraction failed. Query: %s", query), nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s (via DuckDuckGo)", query))

	reSnippet := regexp.MustCompile(`<a class="result__snippet[^"]*".*?>([\s\S]*?)</a>`)
	snippetMatches := reSnippet.FindAllStringSubmatch(html, count+5)

	maxItems := min(len(matches), count)

	for i := 0; i < maxItems; i++ {
		urlStr := matches[i][1]
		title := stripTags(matches[i][2])
		title = strings.TrimSpace(title)

		if strings.Contains(urlStr, "uddg=") {
			if u, err := url.QueryUnescape(urlStr); err == nil {
				idx := strings.Index(u, "uddg=")
				if idx != -1 {
					urlStr = u[idx+5:]
				}
			}
		}

		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, title, urlStr))

		if i < len(snippetMatches) {
			snippet := stripTags(snippetMatches[i][1])
			snippet = strings.TrimSpace(snippet)
			if snippet != "" {
				lines = append(lines, fmt.Sprintf("   %s", snippet))
			}
		}
	}

	return strings.Join(lines, "\n"), nil
}

func stripTags(content string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	return re.ReplaceAllString(content, "")
}

// WebSearchTool performs web searches
type WebSearchTool struct {
	provider    SearchProvider
	defaultOpts SearchOptions
}

// WebSearchToolOptions contains configuration for web search
type WebSearchToolOptions struct {
	ExaAPIKey            string
	ExaEnabled           bool
	BraveAPIKey          string
	BraveEnabled         bool
	BraveMaxResults      int
	DuckDuckGoEnabled    bool
	DuckDuckGoMaxResults int
}

// NewWebSearchTool creates a new web search tool
func NewWebSearchTool(opts WebSearchToolOptions) *WebSearchTool {
	var provider SearchProvider
	searchOpts := SearchOptions{
		NumResults: 8,
		Livecrawl:  "fallback",
		Type:       "auto",
	}

	// Priority: Exa > Brave > DuckDuckGo
	if opts.ExaEnabled && opts.ExaAPIKey != "" {
		provider = NewExaSearchProvider(opts.ExaAPIKey)
	} else if opts.BraveEnabled && opts.BraveAPIKey != "" {
		provider = &BraveSearchProvider{apiKey: opts.BraveAPIKey}
		if opts.BraveMaxResults > 0 {
			searchOpts.NumResults = opts.BraveMaxResults
		}
	} else if opts.DuckDuckGoEnabled {
		provider = &DuckDuckGoSearchProvider{}
		if opts.DuckDuckGoMaxResults > 0 {
			searchOpts.NumResults = opts.DuckDuckGoMaxResults
		}
	} else {
		return nil
	}

	return &WebSearchTool{
		provider:    provider,
		defaultOpts: searchOpts,
	}
}

func (t *WebSearchTool) Name() string {
	return "websearch"
}

func (t *WebSearchTool) Description() string {
	return `Search the web using Exa AI - performs real-time web searches and can scrape content from specific URLs
- Provides up-to-date information for current events and recent data
- Supports configurable result counts and returns the content from the most relevant websites
- Use this tool for accessing information beyond knowledge cutoff
- Searches are performed automatically within a single API call

Usage notes:
  - Supports live crawling modes: 'fallback' (backup if cached unavailable) or 'preferred' (prioritize live crawling)
  - Search types: 'auto' (balanced), 'fast' (quick results), 'deep' (comprehensive search)
  - Configurable context length for optimal LLM integration

The current year is 2026. You MUST use this year when searching for recent information or current events
- Example: If the current year is 2026 and the user asks for "latest AI news", search for "AI news 2026", NOT "AI news 2025"`
}

func (t *WebSearchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Web search query",
			},
			"numResults": map[string]interface{}{
				"type":        "integer",
				"description": "Number of search results to return (default: 8)",
				"minimum":     1.0,
				"maximum":     20.0,
			},
			"livecrawl": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"fallback", "preferred"},
				"description": "Live crawl mode - 'fallback': use live crawling as backup if cached content unavailable, 'preferred': prioritize live crawling",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"auto", "fast", "deep"},
				"description": "Search type - 'auto': balanced search (default), 'fast': quick results, 'deep': comprehensive search",
			},
			"contextMaxCharacters": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum characters for context string optimized for LLMs (default: 10000)",
				"minimum":     1000.0,
				"maximum":     50000.0,
			},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return ErrorResult("query is required")
	}

	opts := t.defaultOpts

	if numResults, ok := args["numResults"].(float64); ok && numResults > 0 {
		opts.NumResults = int(numResults)
	}
	if livecrawl, ok := args["livecrawl"].(string); ok && livecrawl != "" {
		opts.Livecrawl = livecrawl
	}
	if searchType, ok := args["type"].(string); ok && searchType != "" {
		opts.Type = searchType
	}
	if maxChars, ok := args["contextMaxCharacters"].(float64); ok && maxChars > 0 {
		opts.ContextMaxCharacters = int(maxChars)
	}

	result, err := t.provider.Search(ctx, query, opts)
	if err != nil {
		return ErrorResult(fmt.Sprintf("search failed: %v", err))
	}

	return &ToolResult{
		ForLLM:  result,
		ForUser: result,
	}
}
