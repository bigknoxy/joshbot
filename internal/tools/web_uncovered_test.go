package tools

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestWebTool builds a WebTool whose HTTP client is pinned to a local test
// server while the SSRF checks still see a public-looking hostname.
//
// The production client dials through guardedDialControl, which refuses
// loopback — correctly. Tests therefore substitute the transport rather than
// weakening the guard, and stub resolveIP so validateURLForSSRF sees a public
// answer for the fake hostname. Everything else — scheme checks, blocked
// hostnames, redirect validation — stays exactly as production has it.
func newTestWebTool(t *testing.T, srv *httptest.Server) *WebTool {
	t.Helper()
	target := srv.Listener.Addr().String()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, target)
		},
		// The test server uses a self-signed certificate; the hostname is
		// fictional on purpose so the SSRF checks see a name, not a literal.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only transport
	}
	return &WebTool{
		httpClient: &http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxRetries: 1,
		baseDelay:  time.Millisecond,
		resolveIP: func(host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		},
	}
}

func TestWebTool_ExecuteUnknownOperation(t *testing.T) {
	tool := &WebTool{}
	res := tool.Execute(context.Background(), map[string]any{"operation": "rm_rf"})
	if res.Error == nil || !strings.Contains(res.Error.Error(), "unknown operation") {
		t.Fatalf("unknown operation must be refused, got %+v", res)
	}
	// A missing operation is not a licence to guess one.
	res = tool.Execute(context.Background(), map[string]any{})
	if res.Error == nil {
		t.Fatalf("a missing operation must be refused, got output %q", res.Output)
	}
}

// Every advertised operation must reject a call with no query/url rather than
// reaching the network with an empty search.
func TestWebTool_OperationsRequireTheirArgument(t *testing.T) {
	tool := &WebTool{exaCLIAvailable: false}
	tests := []struct {
		operation string
		wantErr   string
	}{
		{"web_search", "query is required"},
		{"web_code", "query is required"},
		{"web_company", "query is required"},
		{"web_research", "query is required"},
		{"web_fetch", "url is required"},
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			res := tool.Execute(context.Background(), map[string]any{"operation": tt.operation})
			if res.Error == nil {
				t.Fatalf("expected an error, got output %q", res.Output)
			}
			if !strings.Contains(res.Error.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", res.Error, tt.wantErr)
			}
		})
	}
}

// The exa-only operations must say exa-cli is missing and how to install it,
// not fail with a bare exec error the model will retry forever.
func TestWebTool_ExaOnlyOperationsReportMissingCLI(t *testing.T) {
	tool := &WebTool{exaCLIAvailable: false}
	for _, op := range []string{"web_code", "web_company", "web_research"} {
		t.Run(op, func(t *testing.T) {
			res := tool.Execute(context.Background(), map[string]any{"operation": op, "query": "go generics"})
			if res.Error == nil {
				t.Fatalf("expected an error, got %q", res.Output)
			}
			if !strings.Contains(res.Error.Error(), "exa-cli is not installed") {
				t.Errorf("error = %q, want the exa-cli install guidance", res.Error)
			}
			if !strings.Contains(res.Error.Error(), "npm install -g exa-cli") {
				t.Errorf("error = %q, want an actionable install command", res.Error)
			}
		})
	}
}

func TestWebAlias_ExecuteInjectsItsOperation(t *testing.T) {
	web := &WebTool{}
	for _, name := range []string{"web_search", "web_fetch", "web_code", "web_company", "web_research"} {
		t.Run(name, func(t *testing.T) {
			alias := &webAlias{web: web, name: name}
			if alias.Name() != name {
				t.Errorf("Name() = %q, want %q", alias.Name(), name)
			}
			if !strings.HasPrefix(alias.Description(), name+": ") {
				t.Errorf("Description() = %q, want the tool-name prefix", alias.Description())
			}
			if len(alias.Parameters()) != len(web.Parameters()) {
				t.Error("an alias must expose the same parameters as the underlying web tool")
			}
			// The alias must set operation itself; if it forwarded the caller's
			// args unchanged, this would come back "unknown operation".
			res := alias.Execute(context.Background(), map[string]any{})
			if res.Error == nil {
				t.Fatalf("expected the missing-argument error, got %q", res.Output)
			}
			if strings.Contains(res.Error.Error(), "unknown operation") {
				t.Errorf("alias %q did not inject its operation: %v", name, res.Error)
			}
		})
	}

	t.Run("caller cannot override the operation", func(t *testing.T) {
		alias := &webAlias{web: web, name: "web_fetch"}
		// A model (or an injected instruction) passing operation=web_search to
		// the web_fetch alias must still be treated as a fetch.
		res := alias.Execute(context.Background(), map[string]any{"operation": "web_search", "query": "x"})
		if res.Error == nil || !strings.Contains(res.Error.Error(), "url is required") {
			t.Fatalf("the alias name must win over a caller-supplied operation, got %+v", res)
		}
	})

	t.Run("unknown alias name falls back to the web description", func(t *testing.T) {
		alias := &webAlias{web: web, name: "mystery"}
		if alias.Description() != web.Description() {
			t.Errorf("Description() = %q, want the web tool's own description", alias.Description())
		}
	})
}

func TestWebTool_FetchBlocksNonPublicTargets(t *testing.T) {
	tool := &WebTool{resolveIP: func(string) ([]net.IP, error) { return nil, fmt.Errorf("nxdomain") }}
	for _, target := range []string{
		"http://127.0.0.1/secrets",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
		"localhost/admin",
		"http://metadata.google.internal/",
	} {
		t.Run(target, func(t *testing.T) {
			res := tool.Execute(context.Background(), map[string]any{"operation": "web_fetch", "url": target})
			if res.Error == nil {
				t.Fatalf("web_fetch(%q) must be blocked, got output %q", target, res.Output)
			}
		})
	}
}

func TestWebTool_FetchHTMLExtractsTitleAndStripsScripts(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>  Hello Title  </title>
<script>var leaked = "SCRIPTSECRET";</script>
<style>body{content:"STYLESECRET"}</style></head>
<body><!-- COMMENTSECRET --><p>Visible body text.</p></body></html>`)
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	res := tool.Execute(context.Background(), map[string]any{"operation": "web_fetch", "url": "https://example.test/page"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Output, "Title: Hello Title") {
		t.Errorf("output should carry the trimmed title, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "URL: https://example.test/page") {
		t.Errorf("output should name the URL it fetched, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "Visible body text.") {
		t.Errorf("body text was lost:\n%s", res.Output)
	}
	// No markup survives: every "<...>" is replaced, including the script and
	// style tags removeTag targets and the comment opener.
	if strings.ContainsAny(res.Output, "<>") {
		t.Errorf("HTML markup must be stripped:\n%s", res.Output)
	}

	// Script and style bodies must be fully stripped — removeTag now removes
	// the complete element, not just the opening tag. Neither a leaked script
	// var nor a style body must reach the model context.
	if strings.Contains(res.Output, "SCRIPTSECRET") {
		t.Errorf("script bodies must be stripped:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "STYLESECRET") {
		t.Errorf("style bodies must be stripped:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "COMMENTSECRET") {
		t.Errorf("HTML comment bodies must not reach the model:\n%s", res.Output)
	}
}

func TestWebTool_FetchPlainTextIsReturnedVerbatim(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "line one\nline two")
	}))
	defer srv.Close()

	res := newTestWebTool(t, srv).Execute(context.Background(),
		map[string]any{"operation": "web_fetch", "url": "https://example.test/notes.txt"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Output, "Content-Type: text/plain") {
		t.Errorf("plain text output should declare its content type, got %q", res.Output)
	}
	if !strings.Contains(res.Output, "line one\nline two") {
		t.Errorf("plain text must be returned as-is, got %q", res.Output)
	}
}

func TestWebTool_FetchTruncatesLargeBodies(t *testing.T) {
	big := strings.Repeat("a", 20000)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, big)
	}))
	defer srv.Close()

	res := newTestWebTool(t, srv).Execute(context.Background(),
		map[string]any{"operation": "web_fetch", "url": "https://example.test/big.txt"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if !strings.Contains(res.Output, "(truncated") {
		t.Errorf("an oversized body must be truncated and say so, got %d chars", len(res.Output))
	}
	if len(res.Output) > 6000 {
		t.Errorf("truncated output is %d chars, far past the 5000-char cap", len(res.Output))
	}
}

func TestWebTool_FetchNonOKStatusIsAnError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	res := newTestWebTool(t, srv).Execute(context.Background(),
		map[string]any{"operation": "web_fetch", "url": "https://example.test/private"})
	if res.Error == nil {
		t.Fatalf("a 403 must surface as an error, not as content: %q", res.Output)
	}
	if !strings.Contains(res.Error.Error(), "403") {
		t.Errorf("error = %q, want it to name the status", res.Error)
	}
}

func TestWebTool_ParseSearchResults(t *testing.T) {
	tool := &WebTool{}
	html := strings.Join([]string{
		`<div class="result">`,
		`<a class="result__a" href="https://example.com/a%3Fq%3D1%26r%3D2">First <em>Title</em></a>`,
		`<div class="result__snippet">a snippet</div>`,
		`</div><!-- result__body -->`,
		`<div class="result">`,
		`<a class="result__a" href="https://example.com/b">Second Title</a>`,
		`<div class="result__snippet">another snippet</div>`,
		`</div><!-- result__body -->`,
	}, "\n")

	got := tool.parseSearchResults(html, 5)
	if len(got) != 2 {
		t.Fatalf("parsed %d results, want 2: %+v", len(got), got)
	}
	if got[0].Title != "First Title" {
		t.Errorf("title = %q, want %q (with <em> stripped)", got[0].Title, "First Title")
	}
	// The percent-encoded query separators must be decoded, otherwise the URL
	// handed back to the model is not fetchable.
	if got[0].URL != "https://example.com/a?q=1&r=2" {
		t.Errorf("url = %q, want the decoded form", got[0].URL)
	}
	if !strings.Contains(got[0].Snippet, "a snippet") {
		t.Errorf("snippet = %q", got[0].Snippet)
	}
	if got[1].Title != "Second Title" {
		t.Errorf("second title = %q", got[1].Title)
	}

	t.Run("maxResults is a hard cap", func(t *testing.T) {
		capped := tool.parseSearchResults(html, 1)
		if len(capped) != 1 {
			t.Errorf("got %d results with maxResults=1, want 1", len(capped))
		}
	})

	t.Run("no results in unrelated HTML", func(t *testing.T) {
		if got := tool.parseSearchResults("<html><body>nothing here</body></html>", 5); len(got) != 0 {
			t.Errorf("got %d results from HTML with no result blocks: %+v", len(got), got)
		}
	})
}

func TestWebTool_DoSearchStatusHandling(t *testing.T) {
	resultHTML := strings.Join([]string{
		`<div class="result">`,
		`<a class="result__a" href="https://example.com/x">A Result</a>`,
		`</div><!-- result__body -->`,
	}, "\n")

	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    string
		wantOutput string
	}{
		{"200 with results", http.StatusOK, resultHTML, "", "A Result"},
		{"200 with no results", http.StatusOK, "<html></html>", "", "No search results found"},
		{"404", http.StatusNotFound, "", "404", ""},
		{"429 after retries", http.StatusTooManyRequests, "", "429", ""},
		{"503 after retries", http.StatusServiceUnavailable, "", "temporarily unavailable", ""},
		{"202 without Location", http.StatusAccepted, "", "without Location header", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			tool := newTestWebTool(t, srv)
			tool.maxRetries = 0 // keep the retry backoff out of the test clock
			res := tool.doSearch("https://example.test/search?q=x", 5)

			if tt.wantErr == "" {
				if res.Error != nil {
					t.Fatalf("unexpected error: %v", res.Error)
				}
				if !strings.Contains(res.Output, tt.wantOutput) {
					t.Errorf("output = %q, want it to contain %q", res.Output, tt.wantOutput)
				}
				return
			}
			if res.Error == nil {
				t.Fatalf("expected an error, got output %q", res.Output)
			}
			if !strings.Contains(res.Error.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", res.Error, tt.wantErr)
			}
		})
	}
}

// A 202/302 Location header is chosen by the search engine, so it gets the same
// SSRF check as any other URL rather than being followed on trust.
func TestWebTool_DoSearchRefusesPrivateRedirect(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest/meta-data/")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.maxRetries = 0
	res := tool.doSearch("https://example.test/search?q=x", 5)
	if res.Error == nil {
		t.Fatalf("a redirect to the metadata endpoint must be refused, got %q", res.Output)
	}
	if !strings.Contains(res.Error.Error(), "refusing to follow redirect") {
		t.Errorf("error = %q, want the redirect refusal", res.Error)
	}
}

// duckDuckGoSearch walks its engine list; when every engine fails the caller
// must get one aggregated, actionable error rather than an empty success.
func TestWebTool_DuckDuckGoSearchAllEnginesFail(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusNotFound)
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.maxRetries = 0
	res := tool.duckDuckGoSearch("some query", 3)
	if res.Error == nil {
		t.Fatalf("expected an error when every engine fails, got %q", res.Output)
	}
	if !strings.Contains(res.Error.Error(), "all search engines failed") {
		t.Errorf("error = %q, want the aggregated failure message", res.Error)
	}
	if !strings.Contains(res.Error.Error(), "web_fetch") {
		t.Errorf("error = %q, want the suggested next step", res.Error)
	}
}

func TestWebTool_DuckDuckGoSearchNamesTheWinningEngine(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<div class=\"result\">\n<a class=\"result__a\" href=\"https://example.com/x\">A Result</a>\n</div><!-- result__body -->")
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.maxRetries = 0
	tool.searchAPI = "https://custom.test/?q=%s"
	res := tool.duckDuckGoSearch("a query", 3)
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	// A configured searchAPI must be tried first, or the operator's own engine
	// is only ever reached when DuckDuckGo is down.
	if !strings.Contains(res.Output, "(Search engine: Custom)") {
		t.Errorf("output = %q, want the custom engine to have been used first", res.Output)
	}
}

func TestWebTool_FormatResults(t *testing.T) {
	tool := &WebTool{}
	if got := tool.formatResults(nil); got.Output != "No search results found" {
		t.Errorf("empty results = %q, want the explicit no-results message", got.Output)
	}
	res := tool.formatResults([]SearchResult{
		{Title: "T1", URL: "https://a", Snippet: "s1", Source: "Exa"},
		{Title: "T2", URL: "https://b", Source: "Exa"},
	})
	for _, want := range []string{"1. T1", "https://a", "s1", "(Source: Exa)", "2. T2", "https://b"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("formatted output missing %q:\n%s", want, res.Output)
		}
	}
	// A result with no snippet must not emit a stray blank snippet line.
	if strings.Contains(res.Output, "T2\n   https://b\n   \n") {
		t.Errorf("empty snippet produced a blank line:\n%q", res.Output)
	}
}

func TestParseExaCLICrawlResult(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
		check   func(t *testing.T, got string)
	}{
		{
			name:  "extracts the first result's text",
			input: `{"results":[{"title":"T","url":"https://a","text":"hello world"}]}`,
			check: func(t *testing.T, got string) {
				if got != "hello world" {
					t.Errorf("got %q, want %q", got, "hello world")
				}
			},
		},
		{
			name:    "malformed JSON is an error, not empty content",
			input:   `{not json`,
			wantErr: "failed to parse exa crawl JSON",
		},
		{
			name:    "no results is an error, not empty content",
			input:   `{"results":[]}`,
			wantErr: "no content extracted",
		},
		{
			name:  "long text is truncated with a marker",
			input: `{"results":[{"text":"` + strings.Repeat("x", 6000) + `"}]}`,
			check: func(t *testing.T, got string) {
				if !strings.Contains(got, "(truncated") {
					t.Error("long crawl text must be truncated and say so")
				}
				if !strings.Contains(got, "6000 chars total") {
					t.Errorf("truncation marker should report the real length, got tail %q", got[len(got)-60:])
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseExaCLICrawlResult(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, got)
		})
	}
}

func TestParseExaResults(t *testing.T) {
	got, err := parseExaResults(`[{"title":"T","url":"https://a","text":"body","summary":"s"}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].Title != "T" || got[0].URL != "https://a" || got[0].Snippet != "body" {
		t.Errorf("mapped result = %+v", got[0])
	}
	if got[0].Source != "Exa" {
		t.Errorf("Source = %q, want Exa — the source label tells the model where the text came from", got[0].Source)
	}

	if _, err := parseExaResults(`not json`); err == nil {
		t.Error("malformed Exa payload must be an error, not silently zero results")
	}
}

func TestWebTool_RemoveTag(t *testing.T) {
	tool := &WebTool{}
	tests := []struct {
		name, html, open, close, want string
	}{
		{
			name: "removes script element with body",
			html: `before<script>var secret = 42;</script>after`,
			open: "<script", close: "</script>",
			want: `beforeafter`,
		},
		{
			name: "removes style element with body",
			html: `pre<style>body{color:red}</style>post`,
			open: "<style", close: "</style>",
			want: `prepost`,
		},
		{
			name: "removes comment with body",
			html: `<!-- secret -->visible`,
			open: "<!--", close: "-->",
			want: `visible`,
		},
		{
			name: "removes multiple occurrences",
			html: `<script>a</script>mid<script>b</script>`,
			open: "<script", close: "</script>",
			want: `mid`,
		},
		{
			name: "script with attributes",
			html: `<script src="x.js">code</script>rest`,
			open: "<script", close: "</script>",
			want: `rest`,
		},
		{
			name: "absent tag is a no-op",
			html: `plain text`,
			open: "<script", close: "</script>",
			want: `plain text`,
		},
		{
			// Tag names are case-insensitive in HTML and the page picks the
			// case, so a case-sensitive strip is a bypass the untrusted side
			// controls: <SCRIPT>…</SCRIPT> would reach the model intact.
			name: "uppercase script element is removed with its body",
			html: `keep<SCRIPT>var x="LEAKED";</SCRIPT>keep2`,
			open: "<script", close: "</script>",
			want: `keepkeep2`,
		},
		{
			name: "mixed-case script element is removed with its body",
			html: `keep<Script src="a.js">LEAKED</ScRiPt>keep2`,
			open: "<script", close: "</script>",
			want: `keepkeep2`,
		},
		{
			name: "uppercase style element is removed with its body",
			html: `keep<STYLE>body{color:LEAKED}</STYLE>keep2`,
			open: "<style", close: "</style>",
			want: `keepkeep2`,
		},
		{
			name: "unterminated opening tag drops remainder",
			html: `text<script`,
			open: "<script", close: "</script>",
			want: `text`,
		},
		{
			name: "no closing tag drops orphan body",
			html: `keep<script>orphan body`,
			open: "<script", close: "</script>",
			want: `keep`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tool.removeTag(tt.html, tt.open, tt.close); got != tt.want {
				t.Errorf("removeTag(%q, %q, %q) = %q, want %q", tt.html, tt.open, tt.close, got, tt.want)
			}
		})
	}
}

func TestNewWebToolFromConfig(t *testing.T) {
	tool := NewWebToolFromConfig(WebToolConfig{})
	if tool.httpClient.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", tool.httpClient.Timeout)
	}
	tool = NewWebToolFromConfig(WebToolConfig{Timeout: 3 * time.Second, SearchAPI: "https://s/?q=%s"})
	if tool.httpClient.Timeout != 3*time.Second {
		t.Errorf("timeout = %v, want the configured 3s", tool.httpClient.Timeout)
	}
	if tool.searchAPI != "https://s/?q=%s" {
		t.Errorf("searchAPI = %q, want the configured value", tool.searchAPI)
	}
	// The dial guard is the real SSRF enforcement point; a client built without
	// it would follow DNS rebinding straight to a private address.
	if tool.httpClient.Transport == nil {
		t.Fatal("web tool must carry a guarded transport, not the default one")
	}
	if _, err := tool.httpClient.Get("http://127.0.0.1:9/"); err == nil {
		t.Error("the configured client must refuse to dial loopback")
	}
}
