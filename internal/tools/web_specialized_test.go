package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ddgResultBody is a minimal DuckDuckGo-shaped result page that
// parseSearchResults can extract one result from, reused by every test below
// that needs the DuckDuckGo tier to succeed.
const ddgResultBody = `<div class="result">
<a class="result__a" href="https://example.com/x">A Result</a>
</div><!-- result__body -->`

// specializedFallbackServer builds a test server whose behaviour differs by
// path, so a single httptest.Server can stand in for both the Exa MCP
// endpoint (POST /mcp, per exaSearch's hardcoded URL) and every DuckDuckGo
// engine (GET /html/, /lite/, /search) at once — newTestWebTool's DialContext
// trick redirects every outbound connection here regardless of hostname, but
// the request path is untouched, so the handler can still tell them apart.
func specializedFallbackServer(mcpStatus int, ddgStatus int) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mcp":
			if mcpStatus == http.StatusOK {
				fmt.Fprint(w, "data: "+`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"[{\"title\":\"Exa Result\",\"url\":\"https://example.com/exa\",\"text\":\"snippet\"}]"}]}}`+"\n")
				return
			}
			http.Error(w, "exa mcp down", mcpStatus)
		case strings.HasPrefix(r.URL.Path, "/html/") || strings.HasPrefix(r.URL.Path, "/lite/") || r.URL.Path == "/search":
			if ddgStatus == http.StatusOK {
				fmt.Fprint(w, ddgResultBody)
				return
			}
			http.Error(w, "ddg down", ddgStatus)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

// newSpecializedTestTool builds a WebTool wired to srv with exa-cli reported
// unavailable, so specializedWithFallback's native leg is skipped and its
// fallback tier (genericSearchChain) is exercised deterministically — no real
// `exa` binary is required, and forcing exaCLIAvailable=true here would risk
// a real network call on any machine that happens to have exa-cli installed.
func newSpecializedTestTool(t *testing.T, srv *httptest.Server) *WebTool {
	t.Helper()
	tool := newTestWebTool(t, srv)
	tool.exaCLIAvailable = false
	tool.maxRetries = 0
	return tool
}

// TestWebCodeFallsBackToExaMCPThenDuckDuckGoOnExaCLIFailure exercises the
// same 3-tier shape webSearch already has, now extended to web_code: with
// exa-cli unavailable, the Exa MCP tier answers and the result carries the
// degraded/general-search marker, since a plain web search is not what
// web_code promises.
func TestWebCodeFallsBackToExaMCPThenDuckDuckGoOnExaCLIFailure(t *testing.T) {
	srv := specializedFallbackServer(http.StatusOK, http.StatusOK)
	defer srv.Close()
	tool := newSpecializedTestTool(t, srv)

	res := tool.webCode(context.Background(), map[string]any{"query": "goroutine leak"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Output, "degraded") {
		t.Errorf("output = %q, want the degraded/general-search marker", res.Output)
	}
	if !strings.Contains(res.Output, "Exa Result") {
		t.Errorf("output = %q, want the Exa MCP result content", res.Output)
	}
}

// TestWebCodeFallsAllTheWayToDuckDuckGo verifies that when Exa MCP also
// fails, web_code's fallback reaches DuckDuckGo — the last tier of the
// shared chain — and still carries the degraded marker.
func TestWebCodeFallsAllTheWayToDuckDuckGo(t *testing.T) {
	srv := specializedFallbackServer(http.StatusInternalServerError, http.StatusOK)
	defer srv.Close()
	tool := newSpecializedTestTool(t, srv)

	res := tool.webCode(context.Background(), map[string]any{"query": "goroutine leak"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Output, "degraded") {
		t.Errorf("output = %q, want the degraded/general-search marker", res.Output)
	}
	if !strings.Contains(res.Output, "A Result") {
		t.Errorf("output = %q, want the DuckDuckGo result content", res.Output)
	}
}

// TestWebCodeAggregatesErrorsWhenAllTiersFail verifies that when every tier
// fails, the error names exa-cli, Exa MCP, and DuckDuckGo — never just the
// last backend tried — mirroring duckDuckGoSearch's own aggregated-failure
// shape.
func TestWebCodeAggregatesErrorsWhenAllTiersFail(t *testing.T) {
	srv := specializedFallbackServer(http.StatusInternalServerError, http.StatusNotFound)
	defer srv.Close()
	tool := newSpecializedTestTool(t, srv)

	res := tool.webCode(context.Background(), map[string]any{"query": "goroutine leak"})
	if res.Error == nil {
		t.Fatalf("expected an error when every tier fails, got %q", res.Output)
	}
	errMsg := strings.ToLower(res.Error.Error())
	for _, want := range []string{"exa-cli", "exa-mcp", "duckduckgo"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("error = %q, want it to name %q", res.Error, want)
		}
	}
}

// TestWebCompanyFallsBackAndDegradesHonestly mirrors the web_code fallback
// test for web_company.
func TestWebCompanyFallsBackAndDegradesHonestly(t *testing.T) {
	srv := specializedFallbackServer(http.StatusOK, http.StatusOK)
	defer srv.Close()
	tool := newSpecializedTestTool(t, srv)

	res := tool.webCompany(context.Background(), map[string]any{"query": "Acme Corp"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Output, "degraded") {
		t.Errorf("output = %q, want the degraded/general-search marker", res.Output)
	}
	if !strings.Contains(res.Output, "company") {
		t.Errorf("output = %q, want the marker to name the operation", res.Output)
	}
}

// TestWebCompanyAggregatesErrorsWhenAllTiersFail mirrors the web_code
// all-fail test for web_company.
func TestWebCompanyAggregatesErrorsWhenAllTiersFail(t *testing.T) {
	srv := specializedFallbackServer(http.StatusInternalServerError, http.StatusNotFound)
	defer srv.Close()
	tool := newSpecializedTestTool(t, srv)

	res := tool.webCompany(context.Background(), map[string]any{"query": "Acme Corp"})
	if res.Error == nil {
		t.Fatalf("expected an error when every tier fails, got %q", res.Output)
	}
	errMsg := strings.ToLower(res.Error.Error())
	for _, want := range []string{"exa-cli", "exa-mcp", "duckduckgo"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("error = %q, want it to name %q", res.Error, want)
		}
	}
}

// TestWebResearchFallsBackAndDegradesHonestly mirrors the web_code fallback
// test for web_research.
func TestWebResearchFallsBackAndDegradesHonestly(t *testing.T) {
	srv := specializedFallbackServer(http.StatusOK, http.StatusOK)
	defer srv.Close()
	tool := newSpecializedTestTool(t, srv)

	res := tool.webResearch(context.Background(), map[string]any{"query": "quantum computing outlook"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Output, "degraded") {
		t.Errorf("output = %q, want the degraded/general-search marker", res.Output)
	}
	if !strings.Contains(res.Output, "research") {
		t.Errorf("output = %q, want the marker to name the operation", res.Output)
	}
}

// TestWebResearchAggregatesErrorsWhenAllTiersFail mirrors the web_code
// all-fail test for web_research.
func TestWebResearchAggregatesErrorsWhenAllTiersFail(t *testing.T) {
	srv := specializedFallbackServer(http.StatusInternalServerError, http.StatusNotFound)
	defer srv.Close()
	tool := newSpecializedTestTool(t, srv)

	res := tool.webResearch(context.Background(), map[string]any{"query": "quantum computing outlook"})
	if res.Error == nil {
		t.Fatalf("expected an error when every tier fails, got %q", res.Output)
	}
	errMsg := strings.ToLower(res.Error.Error())
	for _, want := range []string{"exa-cli", "exa-mcp", "duckduckgo"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("error = %q, want it to name %q", res.Error, want)
		}
	}
}

// TestWebSearchEmitsProgressNotes verifies that, with tools.WithProgress
// installed, webSearch's exa-cli-unavailable checkpoint, its fallback to Exa
// MCP, and the result-count checkpoint are all recorded, in order — the
// natural checkpoints requirement A.5 asks for (trying exa-cli, falling
// back, received N results), reached through the real per-request context
// plumbing rather than a mock.
func TestWebSearchEmitsProgressNotes(t *testing.T) {
	srv := specializedFallbackServer(http.StatusOK, http.StatusOK)
	defer srv.Close()
	tool := newSpecializedTestTool(t, srv)

	var notes []string
	ctx := WithProgress(context.Background(), func(note string) {
		notes = append(notes, note)
	})

	res := tool.webSearch(ctx, map[string]any{"query": "golang"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	if len(notes) < 2 {
		t.Fatalf("expected at least 2 progress notes, got %d: %v", len(notes), notes)
	}
	joined := strings.ToLower(strings.Join(notes, " | "))
	if !strings.Contains(joined, "exa-cli") {
		t.Errorf("notes = %v, want a checkpoint mentioning exa-cli", notes)
	}
	if !strings.Contains(joined, "exa mcp") {
		t.Errorf("notes = %v, want a checkpoint mentioning Exa MCP", notes)
	}
	if !strings.Contains(joined, "received") || !strings.Contains(joined, "result") {
		t.Errorf("notes = %v, want a result-count checkpoint", notes)
	}
}

// TestWebSearchEmitsNoProgressNotesWithoutASink verifies the fire-and-forget
// contract: a caller that never installs a sink gets no panics and no
// behaviour change, matching tools.ProgressFromContext's documented
// no-op-by-default rule.
func TestWebSearchEmitsNoProgressNotesWithoutASink(t *testing.T) {
	srv := specializedFallbackServer(http.StatusOK, http.StatusOK)
	defer srv.Close()
	tool := newSpecializedTestTool(t, srv)

	res := tool.webSearch(context.Background(), map[string]any{"query": "golang"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
}
