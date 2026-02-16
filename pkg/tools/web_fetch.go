package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
)

const (
	webFetchUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
	maxResponseSize   = 5 * 1024 * 1024 // 5MB
	defaultTimeout    = 30 * time.Second
	maxTimeout        = 120 * time.Second
	defaultMaxChars   = 50000
)

// WebFetchTool fetches web content in various formats
type WebFetchTool struct {
	defaultMaxChars int
}

// NewWebFetchTool creates a new web fetch tool
func NewWebFetchTool(maxChars int) *WebFetchTool {
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	return &WebFetchTool{
		defaultMaxChars: maxChars,
	}
}

func (t *WebFetchTool) Name() string {
	return "webfetch"
}

func (t *WebFetchTool) Description() string {
	return `Fetch a URL and extract readable content. Supports multiple formats including markdown, text, and HTML.
Use this to get weather info, news, articles, or any web content.

Features:
- Automatic HTML to Markdown conversion
- Image fetching as base64 attachments
- Configurable timeout (max 120 seconds)
- Content type detection and appropriate parsing`
}

func (t *WebFetchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The URL to fetch content from",
			},
			"format": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"text", "markdown", "html"},
				"description": "The format to return the content in (text, markdown, or html). Defaults to markdown.",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Optional timeout in seconds (max 120)",
				"minimum":     5.0,
				"maximum":     120.0,
			},
			"maxChars": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum characters to extract (default: 50000)",
				"minimum":     100.0,
				"maximum":     500000.0,
			},
		},
		"required": []string{"url"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	urlStr, ok := args["url"].(string)
	if !ok || urlStr == "" {
		return ErrorResult("url is required")
	}

	// Validate URL
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		return ErrorResult("URL must start with http:// or https://")
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid URL: %v", err))
	}

	if parsedURL.Host == "" {
		return ErrorResult("missing domain in URL")
	}

	// Get format
	format := "markdown"
	if f, ok := args["format"].(string); ok && f != "" {
		format = f
	}

	// Get timeout
	timeout := defaultTimeout
	if to, ok := args["timeout"].(float64); ok && to > 0 {
		reqTimeout := time.Duration(to) * time.Second
		if reqTimeout > maxTimeout {
			reqTimeout = maxTimeout
		}
		timeout = reqTimeout
	}

	// Get max chars
	maxChars := t.defaultMaxChars
	if mc, ok := args["maxChars"].(float64); ok && mc > 0 {
		maxChars = int(mc)
	}

	// Build Accept header based on requested format
	acceptHeader := "*/*"
	switch format {
	case "markdown":
		acceptHeader = "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
	case "text":
		acceptHeader = "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
	case "html":
		acceptHeader = "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
	default:
		acceptHeader = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"
	}

	headers := map[string]string{
		"User-Agent":      webFetchUserAgent,
		"Accept":          acceptHeader,
		"Accept-Language": "en-US,en;q=0.9",
	}

	// Create request with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %v", err))
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after 5 redirects")
			}
			return nil
		},
	}

	// Initial request
	resp, err := client.Do(req)
	if err != nil {
		// If timeout or context cancelled
		if ctx.Err() == context.DeadlineExceeded {
			return ErrorResult("request timed out")
		}
		return ErrorResult(fmt.Sprintf("request failed: %v", err))
	}

	// Retry with honest UA if blocked by Cloudflare
	if resp.StatusCode == 403 && resp.Header.Get("cf-mitigated") == "challenge" {
		resp.Body.Close()
		req.Header.Set("User-Agent", "opencode")
		resp, err = client.Do(req)
		if err != nil {
			return ErrorResult(fmt.Sprintf("retry request failed: %v", err))
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ErrorResult(fmt.Sprintf("request failed with status code: %d", resp.StatusCode))
	}

	// Check content length
	contentLength := resp.Header.Get("content-length")
	if contentLength != "" {
		var size int
		if _, err := fmt.Sscanf(contentLength, "%d", &size); err == nil && size > maxResponseSize {
			return ErrorResult("response too large (exceeds 5MB limit)")
		}
	}

	// Read response body
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read response: %v", err))
	}

	if len(body) > maxResponseSize {
		return ErrorResult("response too large (exceeds 5MB limit)")
	}

	contentType := resp.Header.Get("content-type")
	mime := ""
	if contentType != "" {
		mime = strings.Split(contentType, ";")[0]
		mime = strings.TrimSpace(strings.ToLower(mime))
	}

	// Check if response is an image
	isImage := strings.HasPrefix(mime, "image/") && mime != "image/svg+xml" && mime != "image/vnd.fastbidsheet"

	if isImage {
		base64Content := base64.StdEncoding.EncodeToString(body)
		result := map[string]interface{}{
			"url":         urlStr,
			"type":        "image",
			"mime":        mime,
			"size":        len(body),
			"base64":      base64Content,
			"description": "Image fetched successfully",
		}
		resultJSON, _ := json.MarshalIndent(result, "", "  ")
		return &ToolResult{
			ForLLM:  fmt.Sprintf("Fetched image from %s (%s, %d bytes)", urlStr, mime, len(body)),
			ForUser: string(resultJSON),
		}
	}

	// Convert to string for text processing
	content := string(body)

	// Handle content based on requested format and actual content type
	var output string
	var extractor string

	switch format {
	case "markdown":
		if strings.Contains(contentType, "text/html") || looksLikeHTML(content) {
			markdown, err := convertHTMLToMarkdown(content)
			if err != nil {
				// Fallback to text extraction
				output = extractTextFromHTML(content)
				extractor = "text"
			} else {
				output = markdown
				extractor = "markdown"
			}
		} else {
			output = content
			extractor = "raw"
		}

	case "text":
		if strings.Contains(contentType, "text/html") || looksLikeHTML(content) {
			output = extractTextFromHTML(content)
			extractor = "text"
		} else if strings.Contains(contentType, "application/json") {
			// Pretty print JSON
			var jsonData interface{}
			if err := json.Unmarshal(body, &jsonData); err == nil {
				formatted, _ := json.MarshalIndent(jsonData, "", "  ")
				output = string(formatted)
			} else {
				output = content
			}
			extractor = "json"
		} else {
			output = content
			extractor = "raw"
		}

	case "html":
		output = content
		extractor = "html"

	default:
		output = content
		extractor = "raw"
	}

	// Truncate if needed
	truncated := false
	if len(output) > maxChars {
		output = output[:maxChars]
		truncated = true
	}

	result := map[string]interface{}{
		"url":       urlStr,
		"status":    resp.StatusCode,
		"extractor": extractor,
		"truncated": truncated,
		"length":    len(output),
		"text":      output,
	}

	if contentType != "" {
		result["contentType"] = contentType
	}

	resultJSON, _ := json.MarshalIndent(result, "", "  ")

	return &ToolResult{
		ForLLM:  fmt.Sprintf("Fetched %d bytes from %s (extractor: %s, truncated: %v)", len(output), urlStr, extractor, truncated),
		ForUser: string(resultJSON),
	}
}

// looksLikeHTML checks if content looks like HTML
func looksLikeHTML(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.HasPrefix(trimmed, "<!DOCTYPE") ||
		strings.HasPrefix(strings.ToLower(trimmed), "<html") ||
		(strings.Contains(trimmed, "<") && strings.Contains(trimmed, ">"))
}

// convertHTMLToMarkdown converts HTML to Markdown using html-to-markdown library
func convertHTMLToMarkdown(html string) (string, error) {
	converter := md.NewConverter("", true, &md.Options{
		HeadingStyle:     "atx",
		HorizontalRule:   "---",
		BulletListMarker: "-",
		CodeBlockStyle:   "fenced",
		EmDelimiter:      "*",
	})

	// Remove script, style, meta, link tags
	converter.Remove("script", "style", "meta", "link", "noscript", "iframe")

	markdown, err := converter.ConvertString(html)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(markdown), nil
}

// extractTextFromHTML extracts plain text from HTML
func extractTextFromHTML(html string) string {
	// Simple regex-based extraction for plain text
	// Remove script and style tags first
	result := html

	// Remove script tags
	for {
		start := strings.Index(strings.ToLower(result), "<script")
		if start == -1 {
			break
		}
		end := strings.Index(strings.ToLower(result[start:]), "</script>")
		if end == -1 {
			break
		}
		result = result[:start] + result[start+end+9:]
	}

	// Remove style tags
	for {
		start := strings.Index(strings.ToLower(result), "<style")
		if start == -1 {
			break
		}
		end := strings.Index(strings.ToLower(result[start:]), "</style>")
		if end == -1 {
			break
		}
		result = result[:start] + result[start+end+8:]
	}

	// Remove all HTML tags
	var output strings.Builder
	inTag := false
	for _, r := range result {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			output.WriteRune(r)
		}
	}

	text := output.String()

	// Normalize whitespace
	lines := strings.Split(text, "\n")
	var cleanLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}

	return strings.Join(cleanLines, "\n")
}
