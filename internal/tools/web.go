package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/log"
)

// Blocked hosts for SSRF protection
var blockedHosts = map[string]bool{
	"localhost":             true,
	"localhost.localdomain": true,
}

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

const exaCLINotAvailableMsg = `exa-cli is not installed. To use this feature, install exa-cli:
  npm install -g exa-cli
or visit https://github.com/exa-dev/exa-cli`

// Per-tool default budgets for one leg of a web-tool fallback chain, used by
// NewWebTool/NewWebToolFromConfig when the operator has not set
// tools.web.search_timeout etc (zero, per config.Duration's "zero means
// unset" convention — see internal/config/config.go's WebToolsConfig). These
// are starting points grounded in the pre-existing hardcoded values this
// change replaces (exaSearch's 25s, webFetch's 30s), not measured production
// telemetry — there is none yet.
const (
	defaultWebSearchTimeout   = 25 * time.Second
	defaultWebResearchTimeout = 45 * time.Second
	defaultWebCodeTimeout     = 20 * time.Second
	defaultWebCompanyTimeout  = 20 * time.Second
	defaultFinishReserve      = 5 * time.Second

	// minPerCallTimeout is the floor webPerCallDeadline never goes below,
	// even when the turn's remaining budget minus the finish reserve is
	// already exhausted. A zero or negative timeout would make
	// exec.CommandContext refuse to even start the process, turning a
	// "nearly out of time" turn into a guaranteed-empty attempt; a short
	// positive window at least lets the call try and fail with a real error.
	minPerCallTimeout = 1 * time.Second

	// maxSearchResponseBytes caps how much of a DuckDuckGo (or configured
	// custom) search-engine response doSearch will read. Ordinary result
	// pages are a few tens of KB; this exists purely as a backstop against a
	// broken or malicious endpoint streaming an unbounded body, which would
	// otherwise be read to exhaustion by a bare io.ReadAll (observed: an
	// endlessly-streaming test server OOM-killed the process before this cap
	// existed). Mirrors webFetch's pre-existing 100KB cap.
	maxSearchResponseBytes = 2 * 1024 * 1024

	// perEngineSearchTimeout bounds a single search-engine attempt inside
	// duckDuckGoSearch's multi-engine loop. Without it, one hung engine could
	// consume the entire per-op budget genericSearchChain hands to the
	// "duckduckgo" leg (via webPerCallDeadline), leaving nothing for the
	// remaining engines to even be tried. Each engine still gets whatever is
	// actually left of that outer budget when its turn comes — this is only
	// an upper bound per engine, applied via webPerCallDeadline the same way
	// A's per-call deadlines are, so a short remaining budget still clamps
	// down correctly instead of the engine getting the full 10s regardless.
	perEngineSearchTimeout = 10 * time.Second
)

// DefaultWebOperationTimeout returns this package's built-in per-call timeout
// default for one of the four tunable web operations (web_search,
// web_research, web_code, web_company) — the single source of truth also
// used internally by NewWebTool/NewWebToolFromConfig when the operator has
// not set the corresponding tools.web.*_timeout config key. Callers outside
// this package (cmd/joshbot's tuner wiring and `joshbot tuning status`) must
// call this rather than re-declaring the literal values: a duplicated copy
// silently stops matching the moment one of the constants above changes,
// and the tuner would then compute or display bumps relative to a stale
// baseline. Returns 0 for an unrecognized name.
func DefaultWebOperationTimeout(tool string) time.Duration {
	switch tool {
	case "web_search":
		return defaultWebSearchTimeout
	case "web_research":
		return defaultWebResearchTimeout
	case "web_code":
		return defaultWebCodeTimeout
	case "web_company":
		return defaultWebCompanyTimeout
	default:
		return 0
	}
}

// webPerCallDeadline derives a bounded sub-context for one leg of a web-tool
// fallback chain (one exec.CommandContext call, or one HTTP round trip).
//
// When ctx carries a deadline — the normal case, since ctx is the agent
// turn's context — the sub-timeout is the remaining budget minus reserve:
// reserve is time deliberately left over for the ReAct loop to format and
// return a reply after this call returns, so a leg that ran right up to its
// own sub-deadline does not by itself consume the whole remaining turn. The
// result is clamped to maxBudget, which is what stops one leg of a
// multi-tier fallback chain (exa-cli -> Exa MCP -> DuckDuckGo) from eating
// the entire remaining turn budget on its own and leaving nothing for the
// tiers after it.
//
// When ctx carries no deadline at all — a subagent or other programmatic
// caller that never wired one up — there is no remaining budget to derive
// anything from, so staticDefault is used as-is (the per-tool
// config.Duration default; see NewWebToolFromConfig).
//
// The result is floored at minPerCallTimeout in both branches: a remaining
// budget already exhausted still gets a short, positive window rather than
// an instantly (or negatively) expired context.
func webPerCallDeadline(ctx context.Context, maxBudget, staticDefault, reserve time.Duration) (context.Context, context.CancelFunc) {
	var budget time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline) - reserve
		if budget > maxBudget {
			budget = maxBudget
		}
	} else {
		budget = staticDefault
	}
	if budget < minPerCallTimeout {
		budget = minPerCallTimeout
	}
	log.Debug("web tool per-call budget", "budget", budget, "max_budget", maxBudget, "reserve", reserve)
	return context.WithTimeout(ctx, budget)
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

// exaCLISearch performs search via exa-cli binary (primary)
func (t *WebTool) exaCLISearch(ctx context.Context, query string, numResults int) ([]SearchResult, error) {
	if !t.exaCLIAvailable {
		return nil, fmt.Errorf("exa-cli not available")
	}

	cmd := exec.CommandContext(ctx, "exa", "search", query, "--num", strconv.Itoa(numResults), "--format", "json", "--type", "auto")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("exa search failed: %w", err)
	}

	return parseExaCLISearchResults(string(output))
}

// exaCLICrawl fetches and extracts content from a URL using exa-cli
func (t *WebTool) exaCLICrawl(ctx context.Context, url string) (string, error) {
	if !t.exaCLIAvailable {
		return "", fmt.Errorf("exa-cli not available")
	}

	cmd := exec.CommandContext(ctx, "exa", "crawl", url, "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("exa crawl failed: %w", err)
	}

	return parseExaCLICrawlResult(string(output))
}

// parseExaCLICrawlResult parses the JSON output from exa crawl
func parseExaCLICrawlResult(output string) (string, error) {
	var resp struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Text  string `json:"text"`
		} `json:"results"`
		Statuses []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"statuses"`
	}

	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return "", fmt.Errorf("failed to parse exa crawl JSON: %w", err)
	}

	if len(resp.Results) == 0 {
		return "", fmt.Errorf("no content extracted from URL")
	}

	text := resp.Results[0].Text
	if len(text) > 5000 {
		text = text[:5000] + "\n... (truncated, " + strconv.Itoa(len(text)) + " chars total)"
	}
	return text, nil
}

// parseExaCLISearchResults parses JSON output from exa-cli.
// Handles both pretty-printed multi-line JSON (output of `exa search --format json`)
// and single-line JSON per object, separated by blank lines.
func parseExaCLISearchResults(output string) ([]SearchResult, error) {
	var results []SearchResult
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return results, nil
	}

	// exa-cli --format json outputs pretty-printed JSON objects separated by blank lines
	blocks := strings.Split(trimmed, "\n\n")
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var r struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			PublishedDate string `json:"publishedDate"`
			Text          string `json:"text"`
		}
		if err := json.Unmarshal([]byte(block), &r); err != nil {
			log.Debug("Failed to parse exa-cli JSON block", "error", err, "block_len", len(block))
			continue
		}
		results = append(results, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Text,
			Source:  "Exa",
		})
	}
	return results, nil
}

// exaSearch performs search via Exa MCP (free, no API key required).
//
// This deliberately does not go through internal/mcp. That package is a stdio
// client: it spawns a process and speaks JSON-RPC over its pipes, while this is
// a single HTTP POST to one hard-coded endpoint. There is no second MCP client
// implementation here to consolidate — only the wire format is shared.
//
// It also sits outside the internal/mcp trust gate on purpose, and safely: this
// call fetches *search results*, never tool definitions, so nothing it returns
// becomes a callable tool or reaches the system prompt as instructions. Its
// output goes into a tool result like any other web content. If this ever grows
// into "ask the endpoint what tools it has", it must move onto internal/mcp and
// behind the trust store first.
func (t *WebTool) exaSearch(ctx context.Context, query string, numResults int) ([]SearchResult, error) {
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

	// Bound the Exa call within the caller's turn deadline: the agent owns the
	// deadline, so a slow search engine must not outlive the turn and push the
	// whole reply over its budget. WithTimeout pins the ceiling at 25s while the
	// passed ctx already carries (or is capped by) the turn deadline.
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
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
	httpClient      *http.Client
	searchAPI       string
	maxRetries      int
	baseDelay       time.Duration
	exaCLIAvailable bool
	// resolveIP resolves a hostname to its addresses. It exists so tests can
	// drive the SSRF check without depending on real DNS.
	resolveIP func(host string) ([]net.IP, error)

	// Per-operation budgets for webPerCallDeadline, one leg of that
	// operation's fallback chain each. Plain time.Duration, not
	// config.Duration: internal/tools stays decoupled from the config
	// schema, and cmd/joshbot converts at the registry wiring layer (see
	// WebToolBudgets in registry.go), exactly like the pre-existing Timeout
	// field on WebToolConfig already does.
	searchTimeout   time.Duration
	researchTimeout time.Duration
	codeTimeout     time.Duration
	companyTimeout  time.Duration
	// finishReserve is subtracted from the remaining turn budget before
	// deriving a per-call sub-timeout; see webPerCallDeadline.
	finishReserve time.Duration

	// health tracks per-(operation,backend) cooldown across the fallback
	// chains, deprioritizing a repeatedly failing backend without ever
	// dropping it. A WebTool built via a bare struct literal (as several
	// existing tests do) leaves this nil; every access goes through
	// markFailure/markSuccess/orderedBackendsFor, which treat a nil health
	// map as "no cooldown tracking" rather than panicking.
	health *webBackendHealth

	// recorder observes whether each fallback-chain attempt timed out, for
	// the per-tool timeout auto-tuner (internal/tuning). Optional and nil by
	// default — recordOutcome nil-checks before calling it, exactly like
	// markFailure/markSuccess do for health, so a WebTool built via a bare
	// struct literal (as several existing tests do) never panics.
	recorder TimeoutRecorder
}

// recordOutcome tells the configured TimeoutRecorder, if any, whether one
// fallback-chain attempt for op ended because its own per-call deadline
// (webPerCallDeadline) expired. A nil recorder is the common case and this
// is a no-op — the same "optional callback, nil-checked at the call site"
// pattern markFailure/markSuccess already follow for health.
func (t *WebTool) recordOutcome(op string, timedOut bool) {
	if t.recorder != nil {
		t.recorder.Record(op, timedOut)
	}
}

// guardedDialControl refuses to connect to a non-public address.
//
// This runs after DNS resolution and immediately before the socket connects,
// which makes it the real enforcement point: validateURLForSSRF checks a
// hostname up front, but the transport resolves the name again when it dials,
// so a name that answers with a public IP once and a private IP a moment later
// (DNS rebinding) would otherwise slip past the up-front check. It also covers
// request paths that never call validateURLForSSRF at all.
func guardedDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("blocked connection to unparseable address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// The dialer hands us a resolved literal. Anything else is unexpected,
		// so fail closed rather than guess.
		return fmt.Errorf("blocked connection to non-literal address %q", host)
	}
	if isBlockedIP(ip) {
		return fmt.Errorf("blocked connection to non-public address %s", ip)
	}
	return nil
}

// NewWebTool creates a new WebTool.
func NewWebTool(timeout time.Duration, searchAPI string) *WebTool {
	_, err := exec.LookPath("exa")
	exaAvailable := err == nil
	if !exaAvailable {
		log.Debug("exa-cli not found, will use HTTP fallback")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   guardedDialControl,
	}).DialContext

	return &WebTool{
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		searchAPI:       searchAPI,
		maxRetries:      3,
		baseDelay:       1 * time.Second,
		exaCLIAvailable: exaAvailable,
		resolveIP:       net.LookupIP,
		searchTimeout:   defaultWebSearchTimeout,
		researchTimeout: defaultWebResearchTimeout,
		codeTimeout:     defaultWebCodeTimeout,
		companyTimeout:  defaultWebCompanyTimeout,
		finishReserve:   defaultFinishReserve,
		health:          newWebBackendHealth(),
	}
}

// Name returns the name of the tool.
func (t *WebTool) Name() string {
	return "web"
}

// Description returns a description of the tool.
func (t *WebTool) Description() string {
	return `Search and fetch web content. Use web_search/web_fetch/web_code aliases for common operations.`
}

// Parameters returns the parameters for the tool.
func (t *WebTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "operation",
			Type:        ParamString,
			Description: "Operation: web_search, web_code, web_company, web_research, web_fetch",
			Required:    true,
			Enum:        []string{"web_search", "web_code", "web_company", "web_research", "web_fetch"},
		},
		{
			Name:        "query",
			Type:        ParamString,
			Description: "Search query",
			Required:    false,
		},
		{
			Name:        "url",
			Type:        ParamString,
			Description: "URL to fetch",
			Required:    false,
		},
		{
			Name:        "max_results",
			Type:        ParamInteger,
			Description: "Max results (default: 5)",
			Required:    false,
			Default:     5,
		},
	}
}

// Execute runs the web operation.
func (t *WebTool) Execute(ctxArg interface{}, args map[string]any) ToolResult {
	ctx, ok := ctxArg.(context.Context)
	if !ok {
		ctx = context.Background()
	}

	operation, _ := args["operation"].(string)

	switch operation {
	case "web_search":
		return t.webSearch(ctx, args)
	case "web_code":
		return t.webCode(ctx, args)
	case "web_company":
		return t.webCompany(ctx, args)
	case "web_research":
		return t.webResearch(ctx, args)
	case "web_fetch":
		return t.webFetch(ctx, args)
	default:
		return ToolResult{Error: fmt.Errorf("unknown operation: %s", operation)}
	}
}

// noteFunc returns a function that emits a checkpoint note through the
// caller's progress sink (tools.WithProgress), or a no-op when none is
// attached — the common case, since most callers (a subagent, a JSON/headless
// caller with no sink wired) never install one. Fire-and-forget: see
// progress.go's doc comment on why this must never fail closed the way
// Approver does.
func (t *WebTool) noteFunc(ctx context.Context) func(string) {
	progress := ProgressFromContext(ctx)
	if progress == nil {
		return func(string) {}
	}
	return progress
}

// markFailure/markSuccess wrap webBackendHealth's methods with a nil check,
// so a WebTool built via a bare struct literal (several existing tests do
// exactly this) never panics — it just gets no cooldown tracking, which is
// the same as the health map not existing at all.
func (t *WebTool) markFailure(op, backend string) {
	if t.health != nil {
		t.health.markFailure(op, backend)
	}
}

func (t *WebTool) markSuccess(op, backend string) {
	if t.health != nil {
		t.health.markSuccess(op, backend)
	}
}

// searchBackendOrder returns the try order for the three generic-search
// backends, deprioritizing (never dropping) any the health map has recently
// marked failed for op.
func (t *WebTool) searchBackendOrder(op string) []string {
	names := []string{"exa-cli", "exa-mcp", "duckduckgo"}
	if t.health != nil {
		return t.health.orderedBackends(op, names)
	}
	return names
}

// genericSearchChain runs the shared three-tier web-search fallback — exa-cli
// generic search, Exa MCP, DuckDuckGo — in the order the health map currently
// recommends for op, emitting progress notes and updating health as it goes.
// It backs webSearch directly, and also backs webCode/webCompany/webResearch
// as their fallback tier once their own specialized exa-cli subcommand fails:
// op is still "web_code" etc in that case, so a code search's fallback health
// is tracked independently of a plain search's (see TestWebHealthIsPerOperation).
func (t *WebTool) genericSearchChain(ctx context.Context, op, query string, maxResults int, timeout time.Duration, note func(string)) ToolResult {
	var errs []string
	for _, name := range t.searchBackendOrder(op) {
		switch name {
		case "exa-cli":
			if !t.exaCLIAvailable {
				note("exa-cli unavailable, trying Exa MCP")
				continue
			}
			note("trying exa-cli search")
			callCtx, cancel := webPerCallDeadline(ctx, timeout, timeout, t.finishReserve)
			results, err := t.exaCLISearch(callCtx, query, maxResults)
			t.recordOutcome(op, callCtx.Err() == context.DeadlineExceeded)
			cancel()
			if err == nil && len(results) > 0 {
				t.markSuccess(op, name)
				note(fmt.Sprintf("received %d results", len(results)))
				return t.formatResults(results)
			}
			t.markFailure(op, name)
			if err == nil {
				err = errors.New("no results")
			}
			note("exa-cli search failed, falling back to Exa MCP")
			errs = append(errs, fmt.Sprintf("exa-cli: %v", err))

		case "exa-mcp":
			note("trying Exa MCP search")
			callCtx, cancel := webPerCallDeadline(ctx, timeout, timeout, t.finishReserve)
			results, err := t.exaSearch(callCtx, query, maxResults)
			t.recordOutcome(op, callCtx.Err() == context.DeadlineExceeded)
			cancel()
			if err == nil && len(results) > 0 {
				t.markSuccess(op, name)
				note(fmt.Sprintf("received %d results", len(results)))
				return t.formatResults(results)
			}
			t.markFailure(op, name)
			if err == nil {
				err = errors.New("no results")
			}
			note("Exa MCP search failed, falling back to DuckDuckGo")
			errs = append(errs, fmt.Sprintf("exa-mcp: %v", err))

		case "duckduckgo":
			note("trying DuckDuckGo search")
			callCtx, cancel := webPerCallDeadline(ctx, timeout, timeout, t.finishReserve)
			result := t.duckDuckGoSearch(callCtx, query, maxResults)
			t.recordOutcome(op, callCtx.Err() == context.DeadlineExceeded)
			cancel()
			if result.Error == nil {
				t.markSuccess(op, name)
				return result
			}
			t.markFailure(op, name)
			errs = append(errs, fmt.Sprintf("duckduckgo: %v", result.Error))
		}
	}

	return ToolResult{Error: fmt.Errorf("all search backends failed for %s: %s", op, strings.Join(errs, "; "))}
}

// webSearch performs a web search using exa-cli (primary), Exa MCP
// (fallback), or DuckDuckGo (last resort).
func (t *WebTool) webSearch(ctx context.Context, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{Error: errors.New("query is required for web_search")}
	}

	maxResults := 5
	if mr, ok := args["max_results"].(float64); ok {
		maxResults = int(mr)
	}

	return t.genericSearchChain(ctx, "web_search", query, maxResults, t.searchTimeout, t.noteFunc(ctx))
}

// degradedResultPrefix labels a fallback result from
// webCode/webCompany/webResearch's generic-search tier so a caller never
// mistakes an ordinary web search for the specialized result the operation
// promised — a code search and a company/research briefing are read very
// differently from a generic result list.
func degradedResultPrefix(op string) string {
	label := op
	switch op {
	case "web_code":
		label = "code"
	case "web_company":
		label = "company"
	case "web_research":
		label = "research"
	}
	return fmt.Sprintf(
		"Note: this is a degraded/best-effort general web-search result, not a %s-specific result "+
			"(exa-cli's specialized %s search was unavailable or failed).\n\n", label, label)
}

// specializedWithFallback runs one exa-cli specialized subcommand
// (web_code's `exa code`, web_company's `exa company`, web_research's `exa
// research start`) under its own per-call deadline. exa-cli being simply not
// installed is treated the same as the subcommand failing at runtime — both
// are just the native leg not panning out, and both fall through to the same
// place: extending the exa-cli -> Exa MCP -> DuckDuckGo fallback SHAPE
// webSearch already has to these operations, per the design requirement,
// rather than a hard error the moment exa-cli happens to be missing.
//
// On success it returns the native, specialized result untouched. On
// failure it falls through to genericSearchChain (still scoped to op, so its
// own health bookkeeping — including a fresh, redundant-looking exa-cli
// *generic* search attempt — stays independent of a plain web_search's) and
// labels the result as degraded: a labeled generic result is more useful to
// the model mid-outage than a hard failure, so degrade-with-a-marker was
// chosen here over erroring outright (the design's explicitly open
// question) — the marker is what keeps that choice honest. If every leg
// fails, the aggregated error names exa-cli, Exa MCP and DuckDuckGo.
func (t *WebTool) specializedWithFallback(
	ctx context.Context,
	op, query string,
	maxResults int,
	timeout time.Duration,
	note func(string),
	native func(context.Context) ([]SearchResult, error),
	noResultsMsg string,
) ToolResult {
	var nativeErr error
	if t.exaCLIAvailable {
		note("trying exa-cli " + op)
		callCtx, cancel := webPerCallDeadline(ctx, timeout, timeout, t.finishReserve)
		results, err := native(callCtx)
		t.recordOutcome(op, callCtx.Err() == context.DeadlineExceeded)
		cancel()
		if err == nil {
			t.markSuccess(op, "exa-cli")
			if len(results) == 0 {
				return ToolResult{Output: noResultsMsg}
			}
			note(fmt.Sprintf("received %d results", len(results)))
			return t.formatResults(results)
		}
		nativeErr = err
		t.markFailure(op, "exa-cli")
		log.Warn("exa-cli specialized search failed", "op", op, "error", err)
	} else {
		nativeErr = errors.New(exaCLINotAvailableMsg)
	}
	note(fmt.Sprintf("exa-cli %s unavailable or failed, falling back to general web search", op))

	fallback := t.genericSearchChain(ctx, op, query, maxResults, timeout, note)
	if fallback.Error != nil {
		return ToolResult{Error: fmt.Errorf(
			"exa-cli %s failed (%v), and the general web-search fallback also failed: %w",
			op, nativeErr, fallback.Error,
		)}
	}
	fallback.Output = degradedResultPrefix(op) + fallback.Output
	return fallback
}

// webCode performs a code search using exa-cli, falling back to a labeled
// general web search (Exa MCP, then DuckDuckGo) when exa-cli is unavailable
// or its `code` subcommand fails.
func (t *WebTool) webCode(ctx context.Context, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{Error: errors.New("query is required for web_code")}
	}

	maxResults := 5
	if mr, ok := args["max_results"].(float64); ok {
		maxResults = int(mr)
	}

	native := func(callCtx context.Context) ([]SearchResult, error) {
		cmd := exec.CommandContext(callCtx, "exa", "code", query, "--tokens", strconv.Itoa(maxResults*1000), "--format", "json")
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("exa code search failed: %w", err)
		}
		return parseExaCLISearchResults(string(output))
	}

	return t.specializedWithFallback(ctx, "web_code", query, maxResults, t.codeTimeout, t.noteFunc(ctx), native, "No code search results found")
}

// webCompany performs company research using exa-cli, falling back to a
// labeled general web search when exa-cli is unavailable or its `company`
// subcommand fails.
func (t *WebTool) webCompany(ctx context.Context, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{Error: errors.New("query is required for web_company")}
	}

	maxResults := 5
	if mr, ok := args["max_results"].(float64); ok {
		maxResults = int(mr)
	}

	native := func(callCtx context.Context) ([]SearchResult, error) {
		cmd := exec.CommandContext(callCtx, "exa", "company", query, "--num", strconv.Itoa(maxResults), "--format", "json")
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("exa company search failed: %w", err)
		}
		return parseExaCLISearchResults(string(output))
	}

	return t.specializedWithFallback(ctx, "web_company", query, maxResults, t.companyTimeout, t.noteFunc(ctx), native, "No company research results found")
}

// webResearch performs deep research using exa-cli, falling back to a
// labeled general web search when exa-cli is unavailable or its `research`
// subcommand fails.
func (t *WebTool) webResearch(ctx context.Context, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{Error: errors.New("query is required for web_research")}
	}

	maxResults := 5
	if mr, ok := args["max_results"].(float64); ok {
		maxResults = int(mr)
	}

	native := func(callCtx context.Context) ([]SearchResult, error) {
		cmd := exec.CommandContext(callCtx, "exa", "research", "start", query, "--format", "json", "--num", strconv.Itoa(maxResults))
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("exa research failed: %w", err)
		}
		return parseExaCLISearchResults(string(output))
	}

	return t.specializedWithFallback(ctx, "web_research", query, maxResults, t.researchTimeout, t.noteFunc(ctx), native, "No research results found")
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
// It honors ctx for cancellation: blocking backoff yields to the deadline so a
// stalled engine can never outlive the caller's turn budget (that is what
// previously produced "processing your request took too long").
func (t *WebTool) duckDuckGoSearch(ctx context.Context, query string, maxResults int) ToolResult {
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

		// Each engine gets its own bounded sub-timeout carved out of
		// whatever remains of the caller's budget (reserve=0: this is
		// already nested inside genericSearchChain's own per-op deadline,
		// which already reserved time for the turn to finish). Without this,
		// one engine hanging past a reasonable share of the budget would
		// consume the entire per-op deadline and the remaining engines would
		// never even be tried. Loop-level decisions ("did the caller's own
		// deadline expire?") still read the outer ctx below, never engineCtx
		// — a per-engine timeout expiring on its own must fall through to
		// the next engine, not abort the whole search.
		engineCtx, cancel := webPerCallDeadline(ctx, perEngineSearchTimeout, perEngineSearchTimeout, 0)
		result := t.doSearch(engineCtx, searchURL, maxResults)
		cancel()
		if result.Error == nil && result.Output != "" {
			// Success - add engine name to output
			return ToolResult{Output: result.Output + fmt.Sprintf("\n(Search engine: %s)", engine.Name)}
		} else if result.Error != nil && ctx.Err() != nil {
			// The caller's deadline expired; stop walking engines rather than
			// grinding into the next one with no time left.
			return ToolResult{Error: fmt.Errorf("search aborted: %w", ctx.Err())}
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
func (t *WebTool) doSearch(ctx context.Context, searchURL string, maxResults int) ToolResult {
	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
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
			if !sleepWithCtx(ctx, delay) {
				return ToolResult{Error: fmt.Errorf("search aborted: deadline exceeded during retry backoff")}
			}
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
			// A malicious or broken search-engine response could stream an
			// unbounded body; read at most maxSearchResponseBytes rather than
			// buffering the whole thing (mirrors webFetch's existing 100KB
			// cap for the same reason). Truncating is a safe degrade here —
			// result markup is near the top of the page — never an error.
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchResponseBytes))
			if err != nil {
				return ToolResult{Error: fmt.Errorf("failed to read response: %w", err)}
			}
			if len(body) >= maxSearchResponseBytes {
				log.Debug("search response body truncated at cap", "url", req.URL.String(), "limit", maxSearchResponseBytes)
			}
			// A page can answer with malformed or non-UTF8 bytes; sanitize
			// before it reaches parsing or the model, since Go strings do not
			// validate UTF-8 and an invalid byte sequence would otherwise be
			// handed back verbatim in ToolResult.Output.
			body = bytes.ToValidUTF8(body, []byte("�"))

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
				redirectURL, err := t.resolveSearchRedirect(req.URL, location)
				if err != nil {
					return ToolResult{Error: err}
				}
				req.URL = redirectURL
				continue // Retry with new URL
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
				if !sleepWithCtx(ctx, delay) {
					return ToolResult{Error: fmt.Errorf("search aborted: deadline exceeded during rate-limit backoff")}
				}
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

// sleepWithCtx sleeps for d, returning false if ctx is done first. It replaces
// bare time.Sleep in backoff loops so a stalled engine cannot outlive the
// caller's turn deadline.
func sleepWithCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
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
func (t *WebTool) webFetch(ctx context.Context, args map[string]any) ToolResult {
	urlStr, _ := args["url"].(string)
	if urlStr == "" {
		return ToolResult{Error: errors.New("url is required for web_fetch")}
	}

	// Validate URL scheme
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		urlStr = "https://" + urlStr
	}

	// SSRF protection: validate the URL
	if err := t.validateURLForSSRF(urlStr); err != nil {
		return ToolResult{Error: fmt.Errorf("URL blocked by security policy: %w", err)}
	}

	// Try exa crawl first (handles JS-rendered pages)
	if t.exaCLIAvailable {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		content, err := t.exaCLICrawl(ctx, urlStr)
		if err == nil && content != "" {
			return ToolResult{Output: content}
		}
		log.Debug("exa crawl failed, falling back to HTTP fetch", "error", err)
	}

	// Fallback to basic HTTP fetch
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
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
		return t.extractHTMLContent(urlStr, body)
	}

	// For plain text, just return as-is (truncated)
	output := string(body)
	if len(output) > 5000 {
		output = output[:5000] + "\n... (truncated, " + strconv.Itoa(len(output)) + " chars total)"
	}

	return ToolResult{
		Output: fmt.Sprintf("Content-Type: %s\n\n%s", contentType, output),
	}
}

// validateURLForSSRF checks if a URL is safe to fetch (prevents SSRF attacks).
func (t *WebTool) validateURLForSSRF(urlStr string) error {
	// Parse URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Only allow http and https
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("only http and https URLs are allowed, got: %s", parsedURL.Scheme)
	}

	hostname := strings.ToLower(parsedURL.Hostname())

	// Check blocked hosts
	if blockedHosts[hostname] {
		return fmt.Errorf("access to localhost is blocked")
	}

	// Check for IP addresses
	ip := net.ParseIP(hostname)
	if ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("access to non-public IP addresses is blocked: %s", ip.String())
		}
		return nil
	}

	// Names that are unambiguously internal are refused without resolving.
	if isPotentiallyPrivateHostname(hostname) {
		return fmt.Errorf("access to internal hostname %s is blocked", hostname)
	}

	// Every remaining hostname is resolved and checked. This must not be
	// conditional: an attacker controls their own DNS, so the name carries no
	// signal about where it points. A lookup that fails is treated as unsafe
	// rather than safe — we cannot show the target is public, and a fetch we
	// cannot resolve would fail anyway.
	resolve := t.resolveIP
	if resolve == nil {
		resolve = net.LookupIP
	}
	ips, err := resolve(hostname)
	if err != nil {
		return fmt.Errorf("cannot verify %s is safe to fetch: DNS lookup failed: %w", hostname, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("cannot verify %s is safe to fetch: hostname resolved to no addresses", hostname)
	}
	for _, resolvedIP := range ips {
		if isBlockedIP(resolvedIP) {
			return fmt.Errorf("hostname %s resolves to a non-public address: %s", hostname, resolvedIP.String())
		}
	}

	return nil
}

// resolveSearchRedirect returns the URL a search-engine redirect points to,
// or an error if it must not be followed.
//
// The target is attacker-influenced — the search engine picks it, and doSearch
// follows Location headers itself rather than letting the http client do it —
// so it gets the same check as any other URL. Kept separate from doSearch so
// the decision is testable without standing up a server.
func (t *WebTool) resolveSearchRedirect(current *url.URL, location string) (*url.URL, error) {
	next, err := current.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redirect location: %w", err)
	}
	next.Scheme = "https" // Force HTTPS for redirects
	if err := t.validateURLForSSRF(next.String()); err != nil {
		return nil, fmt.Errorf("refusing to follow redirect: %w", err)
	}
	return next, nil
}

// isBlockedIP reports whether an address is anything other than a routable
// public address. It is deliberately broader than isPrivateIP: the metadata
// endpoint every cloud SSRF attack targets (169.254.169.254) is link-local,
// not private, and was missed by the private-range check alone.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if isPrivateIP(ip) {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 0: // 0.0.0.0/8 "this network"
			return true
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127: // 100.64.0.0/10 carrier NAT
			return true
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // 192.0.0.0/24 IETF assignments
			return true
		case ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19): // 198.18.0.0/15 benchmarking
			return true
		case ip4[0] >= 240: // 240.0.0.0/4 reserved, incl. broadcast
			return true
		}
	}
	return false
}

// isPrivateIP checks if an IP is in a private range.
func isPrivateIP(ip net.IP) bool {
	// Convert to 4-byte representation if possible
	ip4 := ip.To4()
	if ip4 != nil {
		// 127.0.0.0/8 (loopback)
		if ip4[0] == 127 {
			return true
		}
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
	}
	// Check IPv6 private ranges (fc00::/7)
	if ip[0] == 0xfc || ip[0] == 0xfd {
		return true
	}
	return false
}

// isPotentiallyPrivateHostname checks if a hostname might resolve to a private IP.
func isPotentiallyPrivateHostname(hostname string) bool {
	// Block known internal/ metadata hostnames
	lowerHost := strings.ToLower(hostname)
	blockedPatterns := []string{
		"localhost",
		"metadata",
		"metadata.google",
		"169.254.169.254",
		"metadata.google.internal",
		"instancemetadata",
		"kubernetes",
		"docker",
		"consul",
		"etcd",
		"zookeeper",
	}
	for _, pattern := range blockedPatterns {
		if strings.Contains(lowerHost, pattern) {
			return true
		}
	}
	return false
}

// extractHTMLContent extracts readable text from HTML.
func (t *WebTool) extractHTMLContent(urlStr string, body []byte) ToolResult {
	html := string(body)

	// Simple extraction: remove scripts, styles, and comments
	// This is a basic implementation
	html = t.removeTag(html, "<script", "</script>")
	html = t.removeTag(html, "<style", "</style>")
	html = t.removeTag(html, "<!--", "-->")

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
	if len(text) > 5000 {
		text = text[:5000] + "\n... (truncated, " + strconv.Itoa(len(text)) + " chars total)"
	}

	var output strings.Builder
	output.WriteString(fmt.Sprintf("URL: %s\n", urlStr))
	if title != "" {
		output.WriteString(fmt.Sprintf("Title: %s\n\n", title))
	}
	output.WriteString(text)

	return ToolResult{Output: output.String()}
}

// removeTag removes all complete instances of an HTML element or comment.
// It finds openTag, then the matching closeTag in the rest of the string
// and deletes the whole span — so the body (script source, CSS, comment
// text) never reaches the model context.
//
// XML-style elements (<script>…</script>) and HTML comments (<!--…-->)
// both work with the same logic:
//   - closeTag is found in `rest` (everything from the start of openTag on)
//   - if no closeTag is found, the remainder drops out (dangling = untrusted)
//
// Matching is ASCII case-insensitive: tag names are case-insensitive in HTML,
// so a case-sensitive search leaves <SCRIPT>…</SCRIPT> bodies intact — and the
// case is chosen by the page, which is exactly the untrusted party this strip
// defends against. Do not narrow this back to strings.Index.
func (t *WebTool) removeTag(html string, openTag, closeTag string) string {
	for {
		start := indexFold(html, openTag)
		if start == -1 {
			break
		}
		rest := html[start:]
		closePos := indexFold(rest, closeTag)
		if closePos == -1 {
			// No closing tag found: drop everything from the open tag onward.
			html = html[:start]
			break
		}
		html = html[:start] + rest[closePos+len(closeTag):]
	}
	return html
}

// WebToolConfig holds configuration for the web tool.
type WebToolConfig struct {
	Timeout   time.Duration
	SearchAPI string

	// SearchTimeout, ResearchTimeout, CodeTimeout and CompanyTimeout bound
	// one leg of the corresponding operation's fallback chain (see
	// webPerCallDeadline). Zero falls back to this package's own default
	// (defaultWebSearchTimeout etc), mirroring the existing Timeout field's
	// "0 defaults in the constructor" convention immediately below.
	SearchTimeout   time.Duration
	ResearchTimeout time.Duration
	CodeTimeout     time.Duration
	CompanyTimeout  time.Duration
	// FinishReserve is subtracted from the remaining turn budget before
	// deriving a per-call sub-timeout. Zero falls back to defaultFinishReserve.
	FinishReserve time.Duration

	// Recorder, if set, observes whether each fallback-chain attempt timed
	// out (see TimeoutRecorder in registry.go). Nil by default: the web tool
	// records nothing unless a caller opts in via WithTimeoutRecorder.
	Recorder TimeoutRecorder
}

// NewWebToolFromConfig creates a WebTool from config.
func NewWebToolFromConfig(cfg WebToolConfig) *WebTool {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	t := NewWebTool(timeout, cfg.SearchAPI)
	if cfg.SearchTimeout > 0 {
		t.searchTimeout = cfg.SearchTimeout
	}
	if cfg.ResearchTimeout > 0 {
		t.researchTimeout = cfg.ResearchTimeout
	}
	if cfg.CodeTimeout > 0 {
		t.codeTimeout = cfg.CodeTimeout
	}
	if cfg.CompanyTimeout > 0 {
		t.companyTimeout = cfg.CompanyTimeout
	}
	if cfg.FinishReserve > 0 {
		t.finishReserve = cfg.FinishReserve
	}
	t.recorder = cfg.Recorder
	return t
}
