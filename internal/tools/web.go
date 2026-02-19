package tools

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WebTool provides web search and fetch capabilities.
type WebTool struct {
	httpClient *http.Client
	searchAPI  string
}

// NewWebTool creates a new WebTool.
func NewWebTool(timeout time.Duration, searchAPI string) *WebTool {
	return &WebTool{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		searchAPI: searchAPI,
	}
}

// Name returns the name of the tool.
func (t *WebTool) Name() string {
	return "web"
}

// Description returns a description of the tool.
func (t *WebTool) Description() string {
	return `Web tools for searching the internet and fetching web content. ` +
		`Use web_search to find information and web_fetch to retrieve specific URLs.`
}

// Parameters returns the parameters for the tool.
func (t *WebTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "operation",
			Type:        ParamString,
			Description: "The operation to perform: web_search or web_fetch",
			Required:    true,
			Enum:        []string{"web_search", "web_fetch"},
		},
		{
			Name:        "query",
			Type:        ParamString,
			Description: "Search query (for web_search)",
			Required:    false,
		},
		{
			Name:        "url",
			Type:        ParamString,
			Description: "URL to fetch (for web_fetch)",
			Required:    false,
		},
		{
			Name:        "max_results",
			Type:        ParamInteger,
			Description: "Maximum number of search results (default: 5)",
			Required:    false,
			Default:     5,
		},
	}
}

// Execute runs the web operation.
func (t *WebTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	operation, _ := args["operation"].(string)

	switch operation {
	case "web_search":
		return t.webSearch(args)
	case "web_fetch":
		return t.webFetch(args)
	default:
		return ToolResult{Error: fmt.Errorf("unknown operation: %s", operation)}
	}
}

// webSearch performs a web search using DuckDuckGo.
func (t *WebTool) webSearch(args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{Error: errors.New("query is required for web_search")}
	}

	maxResults := 5
	if mr, ok := args["max_results"].(float64); ok {
		maxResults = int(mr)
	}

	// Use DuckDuckGo HTML search (no API key required)
	searchURL := fmt.Sprintf(
		"https://html.duckduckgo.com/html/?q=%s",
		strings.ReplaceAll(query, " ", "+"),
	)

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to create request: %w", err)}
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; joshbot/1.0)")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("search request failed: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ToolResult{Error: fmt.Errorf("search returned status %d", resp.StatusCode)}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to read response: %w", err)}
	}

	// Parse results from HTML
	results := t.parseSearchResults(string(body), maxResults)

	if len(results) == 0 {
		return ToolResult{Output: "No search results found"}
	}

	var output strings.Builder
	output.WriteString(fmt.Sprintf("Search results for: %s\n\n", query))

	for i, r := range results {
		output.WriteString(fmt.Sprintf("%d. %s\n", i+1, r.Title))
		output.WriteString(fmt.Sprintf("   %s\n", r.URL))
		if r.Snippet != "" {
			output.WriteString(fmt.Sprintf("   %s\n", r.Snippet))
		}
		output.WriteString("\n")
	}

	return ToolResult{Output: output.String()}
}

// searchResult represents a single search result.
type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// parseSearchResults parses search results from DuckDuckGo HTML.
func (t *WebTool) parseSearchResults(html string, maxResults int) []searchResult {
	var results []searchResult

	// Simple HTML parsing - find result blocks
	// This is a basic implementation; a real parser would use proper HTML parsing
	lines := strings.Split(html, "\n")

	var currentResult *searchResult
	inResult := false

	for _, line := range lines {
		// Look for result class
		if strings.Contains(line, `class="result"`) {
			inResult = true
			currentResult = &searchResult{}
			continue
		}

		if inResult && currentResult != nil {
			// Check for title
			if strings.Contains(line, "class=\"result__a\"") {
				// Extract URL
				hrefIdx := strings.Index(line, "href=\"")
				if hrefIdx != -1 {
					endIdx := strings.Index(line[hrefIdx+6:], "\"")
					if endIdx != -1 {
						currentResult.URL = line[hrefIdx+6 : hrefIdx+6+endIdx]
						// Decode URL
						currentResult.URL = strings.ReplaceAll(currentResult.URL, "%3F", "?")
						currentResult.URL = strings.ReplaceAll(currentResult.URL, "%3D", "=")
						currentResult.URL = strings.ReplaceAll(currentResult.URL, "%26", "&")
					}
				}
			}

			// Check for title text
			if strings.Contains(line, ">") && strings.Contains(line, "</a>") {
				// Extract title between > and </a>
				start := strings.Index(line, ">")
				end := strings.Index(line, "</a>")
				if start != -1 && end != -1 && end > start {
					title := line[start+1 : end]
					// Clean up
					title = strings.TrimSpace(title)
					title = strings.ReplaceAll(title, "<em>", "")
					title = strings.ReplaceAll(title, "</em>", "")
					if currentResult.Title == "" {
						currentResult.Title = title
					}
				}
			}

			// Check for snippet
			if strings.Contains(line, "class=\"result__snippet\"") {
				snippet := strings.TrimSpace(line)
				snippet = strings.ReplaceAll(snippet, "<em>", "")
				snippet = strings.ReplaceAll(snippet, "</em>", "")
				// Remove HTML tags
				snippet = strings.ReplaceAll(snippet, "<a class=\"result__a\" href=\"", "")
				snippet = strings.ReplaceAll(snippet, "</a>", "")
				snippet = strings.ReplaceAll(snippet, "<a class=\"result__snippet\" href=\"", "")
				snippet = strings.TrimSpace(snippet)
				currentResult.Snippet = snippet
			}

			// End of result
			if strings.Contains(line, "</div>") && strings.Contains(line, "result__body") {
				if currentResult != nil && currentResult.Title != "" {
					results = append(results, *currentResult)
					if len(results) >= maxResults {
						break
					}
				}
				inResult = false
				currentResult = nil
			}
		}
	}

	return results
}

// webFetch fetches content from a URL.
func (t *WebTool) webFetch(args map[string]any) ToolResult {
	url, _ := args["url"].(string)
	if url == "" {
		return ToolResult{Error: errors.New("url is required for web_fetch")}
	}

	// Validate URL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to create request: %w", err)}
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; joshbot/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,*/*")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("fetch request failed: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ToolResult{Error: fmt.Errorf("fetch returned status %d", resp.StatusCode)}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024)) // Limit to 100KB
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to read response: %w", err)}
	}

	contentType := resp.Header.Get("Content-Type")

	// For HTML content, extract text
	if strings.Contains(contentType, "text/html") {
		return t.extractHTMLContent(url, body)
	}

	// For plain text, just return as-is (truncated)
	output := string(body)
	if len(output) > 10000 {
		output = output[:10000] + "\n... (truncated)"
	}

	return ToolResult{
		Output: fmt.Sprintf("Content-Type: %s\n\n%s", contentType, output),
	}
}

// extractHTMLContent extracts readable text from HTML.
func (t *WebTool) extractHTMLContent(url string, body []byte) ToolResult {
	html := string(body)

	// Simple extraction: remove scripts, styles, and comments
	// This is a basic implementation
	html = t.removeTag(html, "<script")
	html = t.removeTag(html, "<style")
	html = t.removeTag(html, "<!--")

	// Get title
	title := ""
	titleStart := strings.Index(html, "<title>")
	if titleStart != -1 {
		titleEnd := strings.Index(html, "</title>")
		if titleEnd != -1 {
			title = strings.TrimSpace(html[titleStart+7 : titleEnd])
		}
	}

	// Extract text content (basic)
	// Remove all HTML tags
	text := html
	for {
		tagStart := strings.Index(text, "<")
		if tagStart == -1 {
			break
		}
		tagEnd := strings.Index(text, ">")
		if tagEnd == -1 || tagEnd < tagStart {
			break
		}
		text = text[:tagStart] + " " + text[tagEnd+1:]
	}

	// Clean up whitespace
	lines := strings.Split(text, "\n")
	var cleanLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}
	text = strings.Join(cleanLines, "\n")

	// Limit output
	if len(text) > 15000 {
		text = text[:15000] + "\n... (truncated)"
	}

	var output strings.Builder
	output.WriteString(fmt.Sprintf("URL: %s\n", url))
	if title != "" {
		output.WriteString(fmt.Sprintf("Title: %s\n\n", title))
	}
	output.WriteString(text)

	return ToolResult{Output: output.String()}
}

// removeTag removes all instances of a tag from HTML.
func (t *WebTool) removeTag(html, tag string) string {
	for {
		start := strings.Index(html, tag)
		if start == -1 {
			break
		}
		end := strings.Index(html[start:], ">")
		if end == -1 {
			break
		}
		html = html[:start] + html[start+end+1:]
	}
	return html
}

// WebToolConfig holds configuration for the web tool.
type WebToolConfig struct {
	Timeout   time.Duration
	SearchAPI string
}

// NewWebToolFromConfig creates a WebTool from config.
func NewWebToolFromConfig(cfg WebToolConfig) *WebTool {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return NewWebTool(timeout, cfg.SearchAPI)
}
