package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"
)

// SearchEngine represents a search engine endpoint.
type SearchEngine struct {
	Name   string
	URL    string
	UseGET bool // Some engines require GET instead of POST-style URL
}

// Default search engines in order of preference.
var searchEngines = []SearchEngine{
	{Name: "DuckDuckGo HTML", URL: "https://html.duckduckgo.com/html/?q=%s"},
	{Name: "DuckDuckGo Lite", URL: "https://lite.duckduckgo.com/lite/?q=%s"},
	{Name: "SearXNG", URL: "https://searx.be/search?q=%s"},
}

// exaSearchRequest for JSON-RPC request
type exaSearchRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  struct {
		Name      string `json:"name"`
		Arguments struct {
			Query      string `json:"query"`
			NumResults int    `json:"numResults"`
			Type       string `json:"type"`
		} `json:"arguments"`
	} `json:"params"`
}

// exaSearchResponse for JSON-RPC response (SSE format)
type exaSearchResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

// SearchResult represents a structured search result.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
	Source  string
}

// exaSearch performs search via Exa MCP (free, no API key required)
func (t *WebTool) exaSearch(query string, numResults int) ([]SearchResult, error) {
	// Build JSON-RPC request
	req := exaSearchRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	req.Params.Name = "web_search_exa"
	req.Params.Arguments.Query = query
	req.Params.Arguments.NumResults = numResults
	req.Params.Arguments.Type = "auto"

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://mcp.exa.ai/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	// Execute request
	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exa returned status %d", resp.StatusCode)
	}

	// Parse SSE response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Parse SSE format: lines starting with "data: "
	lines := strings.Split(string(respBody), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var exaResp exaSearchResponse
			if err := json.Unmarshal([]byte(data), &exaResp); err != nil {
				continue
			}
			if len(exaResp.Result.Content) > 0 {
				// Parse the text field which contains JSON array of results
				return parseExaResults(exaResp.Result.Content[0].Text)
			}
		}
	}

	return nil, fmt.Errorf("no results in response")
}

// parseExaResults parses Exa's JSON result string
func parseExaResults(text string) ([]SearchResult, error) {
	// Exa returns results as JSON array in the text field
	var results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Text    string `json:"text"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text), &results); err != nil {
		return nil, fmt.Errorf("parse results: %w", err)
	}

	var searchResults []SearchResult
	for _, r := range results {
		searchResults = append(searchResults, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Text,
			Source:  "Exa",
		})
	}
	return searchResults, nil
}

// WebTool provides web search and fetch capabilities.
type WebTool struct {
	httpClient *http.Client
	searchAPI  string
	// maxRetries is the maximum number of retries for 202 responses
	maxRetries int
	// baseDelay is the base delay for exponential backoff
	baseDelay time.Duration
}

// NewWebTool creates a new WebTool.
func NewWebTool(timeout time.Duration, searchAPI string) *WebTool {
	return &WebTool{
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Don't follow redirects automatically - let us handle them
				return http.ErrUseLastResponse
			},
		},
		searchAPI:  searchAPI,
		maxRetries: 3,
		baseDelay:  1 * time.Second,
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

// webSearch performs a web search using Exa MCP (primary) or DuckDuckGo (fallback)
func (t *WebTool) webSearch(args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{Error: errors.New("query is required for web_search")}
	}

	maxResults := 5
	if mr, ok := args["max_results"].(float64); ok {
		maxResults = int(mr)
	}

	// Try Exa MCP first (free, no API key)
	log.Debug("Trying Exa MCP search", "query", query)
	results, err := t.exaSearch(query, maxResults)
	if err == nil && len(results) > 0 {
		log.Debug("Exa search succeeded", "results", len(results))
		return t.formatResults(results)
	}

	// Log Exa failure and fall back to DuckDuckGo
	if err != nil {
		log.Debug("Exa search failed, falling back to DuckDuckGo", "error", err)
	} else {
		log.Debug("Exa returned no results, falling back to DuckDuckGo")
	}

	// Fallback to DuckDuckGo
	return t.duckDuckGoSearch(query, maxResults)
}

// formatResults formats search results into a ToolResult
func (t *WebTool) formatResults(results []SearchResult) ToolResult {
	if len(results) == 0 {
		return ToolResult{Output: "No search results found"}
	}

	var output strings.Builder
	output.WriteString("Search results:\n\n")

	for i, r := range results {
		output.WriteString(fmt.Sprintf("%d. %s\n", i+1, r.Title))
		output.WriteString(fmt.Sprintf("   %s\n", r.URL))
		if r.Snippet != "" {
			output.WriteString(fmt.Sprintf("   %s\n", r.Snippet))
		}
		output.WriteString(fmt.Sprintf("   (Source: %s)\n", r.Source))
		output.WriteString("\n")
	}

	return ToolResult{Output: output.String()}
}

// duckDuckGoSearch performs a web search using DuckDuckGo with fallbacks.
func (t *WebTool) duckDuckGoSearch(query string, maxResults int) ToolResult {
	// Try each search engine in order
	var lastError error
	engines := searchEngines

	// If custom searchAPI is configured, try it first
	if t.searchAPI != "" {
		engines = append([]SearchEngine{{Name: "Custom", URL: t.searchAPI}}, engines...)
	}

	for _, engine := range engines {
		searchURL := fmt.Sprintf(
			engine.URL,
			strings.ReplaceAll(query, " ", "+"),
		)

		log.Debug("Trying search engine", "engine", engine.Name, "url", searchURL)

		result := t.doSearch(searchURL, maxResults)
		if result.Error == nil && result.Output != "" {
			// Success - add engine name to output
			return ToolResult{Output: result.Output + fmt.Sprintf("\n(Search engine: %s)", engine.Name)}
		}

		// Check if it's a retryable error (202, 429, 5xx)
		errStr := result.Error.Error()
		if strings.Contains(errStr, "status 202") || strings.Contains(errStr, "status 429") ||
			strings.Contains(errStr, "status 5") {
			log.Warn("Search engine returned retryable status", "engine", engine.Name, "error", errStr)
		} else {
			log.Debug("Search engine failed", "engine", engine.Name, "error", errStr)
		}
		lastError = result.Error
	}

	// All engines failed
	return ToolResult{Error: fmt.Errorf(
		"all search engines failed. Last error: %v. "+
			"Try using web_fetch to directly access a URL, or check your network connection.",
		lastError,
	)}
}

// doSearch performs a single search request with retry logic.
func (t *WebTool) doSearch(searchURL string, maxResults int) ToolResult {
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to create request: %w", err)}
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	// Retry loop with exponential backoff for 202/redirect responses
	var lastResp *http.Response
	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			delay := t.baseDelay * time.Duration(1<<(attempt-1))
			log.Debug("Retrying search after delay", "attempt", attempt, "delay", delay)
			time.Sleep(delay)
		}

		resp, err := t.httpClient.Do(req)
		if err != nil {
			return ToolResult{Error: fmt.Errorf("search request failed: %w", err)}
		}

		lastResp = resp

		// Handle different status codes
		switch resp.StatusCode {
		case http.StatusOK:
			// Success - parse and return results
			defer resp.Body.Close()
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
			output.WriteString(fmt.Sprintf("Search results:\n\n"))

			for i, r := range results {
				output.WriteString(fmt.Sprintf("%d. %s\n", i+1, r.Title))
				output.WriteString(fmt.Sprintf("   %s\n", r.URL))
				if r.Snippet != "" {
					output.WriteString(fmt.Sprintf("   %s\n", r.Snippet))
				}
				output.WriteString("\n")
			}

			return ToolResult{Output: output.String()}

		case http.StatusAccepted, http.StatusFound, http.StatusSeeOther:
			// 202 Accepted, 302 Found, 303 See Other - check for redirect
			defer resp.Body.Close()

			// Check for Location header (redirect)
			location := resp.Header.Get("Location")
			if location != "" {
				log.Debug("Following redirect", "location", location)
				// Update request URL to redirect location
				req.URL, err = req.URL.Parse(location)
				if err != nil {
					return ToolResult{Error: fmt.Errorf("failed to parse redirect location: %w", err)}
				}
				req.URL.Scheme = "https" // Force HTTPS for redirects
				continue                 // Retry with new URL
			}

			// No Location header - retry after delay
			if attempt < t.maxRetries {
				log.Debug("Received 202/302 without Location, retrying", "status", resp.StatusCode)
				continue
			}

			return ToolResult{Error: fmt.Errorf(
				"search returned status %d (Accepted/Redirect) without Location header after %d retries",
				resp.StatusCode, t.maxRetries,
			)}

		case http.StatusTooManyRequests:
			// 429 - Too many requests, try next engine or retry
			defer resp.Body.Close()
			if attempt < t.maxRetries {
				// Longer delay for rate limiting
				delay := t.baseDelay * time.Duration(1<<attempt) * 2
				log.Debug("Rate limited, waiting longer", "delay", delay)
				time.Sleep(delay)
				continue
			}
			return ToolResult{Error: fmt.Errorf("search returned status 429 (Too Many Requests)")}

		case http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			// 503, 504 - Server errors, retry
			defer resp.Body.Close()
			if attempt < t.maxRetries {
				continue
			}
			return ToolResult{Error: fmt.Errorf("search engine temporarily unavailable (status %d)", resp.StatusCode)}

		default:
			defer resp.Body.Close()
			return ToolResult{Error: fmt.Errorf(
				"search returned status %d %s. "+
					"Try using web_fetch to directly access a search engine URL.",
				resp.StatusCode, http.StatusText(resp.StatusCode),
			)}
		}
	}

	// Should not reach here, but safety net
	if lastResp != nil {
		defer lastResp.Body.Close()
	}
	return ToolResult{Error: fmt.Errorf("search failed after %d retries", t.maxRetries)}
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
