package api

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func webuiServer(t *testing.T, a Processor) *Server {
	t.Helper()
	s, err := New(a, Options{
		Listen:  "127.0.0.1:0",
		APIKeys: []string{"secret"},
		WebUI:   true,
		Version: "v9.9.9-test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// login performs the real login exchange and returns the cookie and CSRF token.
func login(t *testing.T, s *Server, key string) (*http.Cookie, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, webuiPathLogin, strings.NewReader(`{"key":"`+key+`"}`))
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("login: status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("login body: %v", err)
	}
	if body.CSRF == "" {
		t.Fatal("login returned no CSRF token")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == webuiCookieName {
			return c, body.CSRF
		}
	}
	t.Fatal("login set no session cookie")
	return nil, ""
}

// TestWebUIRoutesAbsentWhenDisabled is the enablement guard. api.webui defaults
// to false, and off must mean the routes do not exist — not that they exist and
// refuse. An HTML login form that accepts a key granting shell access must not
// appear on an existing `joshbot serve` bind at upgrade.
func TestWebUIRoutesAbsentWhenDisabled(t *testing.T) {
	s := testServer(t, &fakeAgent{reply: "hi"})
	for _, path := range []string{"/", webuiPathLogin, webuiPathConfig, webuiPathSession, webuiPathStatic + "app.js"} {
		method := http.MethodGet
		if path == webuiPathLogin {
			method = http.MethodPost
		}
		r := httptest.NewRequest(method, path, strings.NewReader(`{"key":"secret"}`))
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want 404 (webui disabled)", method, path, w.Code)
		}
	}
}

// TestWebUIServesDocumentAndAssetsWhenEnabled proves the embed is real. The
// assets are compiled in, so a wrong embed path is a silent 404 at runtime that
// only a request catches.
func TestWebUIServesDocumentAndAssetsWhenEnabled(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})

	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<title>joshbot</title>") {
		t.Error("GET / did not return the web UI document")
	}
	// No inline script or style, because the CSP has no 'unsafe-inline'. An
	// inline block would render a blank page in a browser and pass a test that
	// only checked the status.
	if strings.Contains(w.Body.String(), "<script>") || strings.Contains(w.Body.String(), "<style>") {
		t.Error("document carries an inline script or style, which the CSP blocks")
	}

	for _, asset := range []string{"app.js", "app.css"} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, webuiPathStatic+asset, nil))
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Errorf("GET %s: status %d, %d bytes", asset, w.Code, w.Body.Len())
		}
	}
}

// TestWebUIDocumentSetsSecurityHeaders pins the CSP and friends. The page
// renders text a model wrote after reading the web; without script-src 'self'
// and no 'unsafe-inline', an injection in that text becomes script execution
// against a session that reaches the shell tool.
func TestWebUIDocumentSetsSecurityHeaders(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP %q allows unsafe inline or eval", csp)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

// TestWebUILoginRejectsWrongKey is the core credential test. The login route is
// the one place a key is presented over a POST body, and it must be exactly as
// strict as the bearer header.
func TestWebUILoginRejectsWrongKey(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	for _, body := range []string{`{"key":"wrong"}`, `{"key":""}`, `{"key":"  "}`, `{}`, `{"key":"secretx"}`, `{"key":"secre"}`} {
		r := httptest.NewRequest(http.MethodPost, webuiPathLogin, strings.NewReader(body))
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("login %s: status %d, want 401", body, w.Code)
		}
		if len(w.Result().Cookies()) != 0 {
			t.Errorf("login %s: set a cookie on failure", body)
		}
	}
}

// TestWebUILoginSetsHardenedCookie pins the cookie flags. Each one is
// load-bearing: HttpOnly is what stops an XSS in the transcript from stealing a
// shell-grade credential, and SameSite=Strict is the first half of the CSRF
// defence.
func TestWebUILoginSetsHardenedCookie(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	c, _ := login(t, s, "secret")

	if !c.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("session cookie Path = %q, want /", c.Path)
	}
	if c.Value == "" || len(c.Value) < 32 {
		t.Errorf("session cookie value %q is too short to be 32 random bytes", c.Value)
	}
	// Plain HTTP: Secure must be off, or the browser drops the cookie on the
	// loopback bind that is the normal case and every later request 401s.
	if c.Secure {
		t.Error("session cookie is Secure over plain HTTP; the browser would drop it")
	}
}

// TestWebUILoginSetsSecureCookieUnderTLS is the other half: over TLS the flag
// must be set, or the cookie is sent in the clear on any downgrade.
func TestWebUILoginSetsSecureCookieUnderTLS(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	r := httptest.NewRequest(http.MethodPost, "https://example.test"+webuiPathLogin, strings.NewReader(`{"key":"secret"}`))
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("login: status %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == webuiCookieName {
			if !c.Secure {
				t.Error("session cookie is not Secure over TLS")
			}
			return
		}
	}
	t.Fatal("no session cookie")
}

// TestCookiePathRequiresCSRFOnWrites is the CSRF guard. Without it, any page the
// operator visits could POST a chat turn from their authenticated browser — and
// a chat turn reaches the shell tool.
func TestCookiePathRequiresCSRFOnWrites(t *testing.T) {
	agentStub := &fakeAgent{reply: "hi"}
	s := webuiServer(t, agentStub)
	c, csrf := login(t, s, "secret")

	post := func(header string) int {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
		r.AddCookie(c)
		if header != "" {
			r.Header.Set(csrfHeader, header)
		}
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		return w.Code
	}

	if got := post(""); got != http.StatusUnauthorized {
		t.Errorf("cookie POST with no CSRF header: status %d, want 401", got)
	}
	if got := post("wrong-token"); got != http.StatusUnauthorized {
		t.Errorf("cookie POST with wrong CSRF token: status %d, want 401", got)
	}
	if got := post(csrf); got != http.StatusOK {
		t.Errorf("cookie POST with the right CSRF token: status %d, want 200", got)
	}
}

// TestBearerPathIgnoresCSRFAndCookies pins the compatibility promise: the WebUI
// added requirements to the cookie path only. An existing OpenAI client sends no
// CSRF header and no Origin, and must keep working unchanged.
func TestBearerPathIgnoresCSRFAndCookies(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Authorization", "Bearer secret")
	// A hostile Origin and no CSRF header: the bearer path must not care, since
	// a bearer credential is not something a browser attaches automatically.
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("bearer POST: status %d body %s", w.Code, w.Body.String())
	}
}

// TestCookiePathRefusesCrossOriginWrite is the cheap guard in front of the token
// check: a cross-origin form post never gets as far as needing a token.
func TestCookiePathRefusesCrossOriginWrite(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	c, csrf := login(t, s, "secret")

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	r.AddCookie(c)
	r.Header.Set(csrfHeader, csrf)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("cross-origin cookie POST: status %d, want 401", w.Code)
	}
}

// TestWebUIConfigServesRealVersionAndCSRF covers mockup defect 2: the version
// badge was hardcoded. The page now renders what the server reports, and the
// CSRF token is served only to the cookie path that needs it.
func TestWebUIConfigServesRealVersionAndCSRF(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	c, csrf := login(t, s, "secret")

	r := httptest.NewRequest(http.MethodGet, webuiPathConfig, nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("config: status %d", w.Code)
	}
	var cfg webuiConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("config body: %v", err)
	}
	if cfg.Version != "v9.9.9-test" {
		t.Errorf("version = %q, want the server's own build version", cfg.Version)
	}
	if cfg.Model != ModelID {
		t.Errorf("model = %q, want %q", cfg.Model, ModelID)
	}
	if cfg.CSRF != csrf {
		t.Errorf("csrf = %q, want the session's token", cfg.CSRF)
	}

	// A bearer caller has no cookie session, so there is no token to serve.
	w2 := do(t, s, http.MethodGet, webuiPathConfig, "secret", "")
	var cfg2 webuiConfigResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &cfg2); err != nil {
		t.Fatalf("config body: %v", err)
	}
	if cfg2.CSRF != "" {
		t.Error("a CSRF token was served to the bearer path, which does not use one")
	}
}

// TestWebUIConfigRefusesUnauthenticated is what the page's boot uses to decide
// whether to show the login gate. A 200 here would hand an anonymous caller the
// server's version.
func TestWebUIConfigRefusesUnauthenticated(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, webuiPathConfig, nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated config: status %d, want 401", w.Code)
	}
}

// TestWebUISessionExpires pins the TTL. A session that never expires is a
// permanent shell-grade credential in a map, and expiry that only hides an entry
// is not expiry — the entry must be gone.
func TestWebUISessionExpires(t *testing.T) {
	ws := newWebUISessions()
	now := time.Now()
	id, _, err := ws.create(now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok := ws.get(id, now.Add(time.Minute)); !ok {
		t.Fatal("session was not live one minute after creation")
	}
	// Past the TTL measured from the last activity.
	if _, ok := ws.get(id, now.Add(time.Minute).Add(webuiSessionTTL).Add(time.Second)); ok {
		t.Fatal("expired session was still accepted")
	}
	if ws.len() != 0 {
		t.Errorf("expired session left %d entries in the map", ws.len())
	}
}

// TestWebUISessionMapIsBounded pins that the map cannot grow without limit. The
// keys are server-minted, so only an authenticated caller can add — but a login
// loop would still pin memory for the process's lifetime.
func TestWebUISessionMapIsBounded(t *testing.T) {
	ws := newWebUISessions()
	base := time.Now()
	for i := 0; i < maxWebUISessions*3; i++ {
		if _, _, err := ws.create(base.Add(time.Duration(i) * time.Millisecond)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if n := ws.len(); n > maxWebUISessions {
		t.Errorf("session map holds %d entries, want at most %d", n, maxWebUISessions)
	}
}

// TestWebUILogoutDropsTheSession: the cookie must stop working, not merely be
// cleared in the browser. A logout that only expires the client copy leaves a
// live server-side session for anyone holding the value.
func TestWebUILogoutDropsTheSession(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	c, _ := login(t, s, "secret")

	r := httptest.NewRequest(http.MethodPost, webuiPathLogout, nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("logout: status %d", w.Code)
	}

	r2 := httptest.NewRequest(http.MethodGet, webuiPathConfig, nil)
	r2.AddCookie(c)
	w2 := httptest.NewRecorder()
	s.routes().ServeHTTP(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("config after logout: status %d, want 401", w2.Code)
	}
}

// TestWebUISessionTranscript covers the reload path: history renders because the
// server reads it back, not because the browser stored it.
func TestWebUISessionTranscript(t *testing.T) {
	s, err := New(&fakeAgent{reply: "hi"}, Options{
		Listen:  "127.0.0.1:0",
		APIKeys: []string{"secret"},
		WebUI:   true,
		Transcript: func(user string) ([]TranscriptMessage, error) {
			if user != "web-abcd1234" {
				t.Errorf("transcript asked for %q", user)
			}
			return []TranscriptMessage{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := do(t, s, http.MethodGet, webuiPathSession+"?user=web-abcd1234", "secret", "")
	if w.Code != http.StatusOK {
		t.Fatalf("session: status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Messages []TranscriptMessage `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("session body: %v", err)
	}
	if len(body.Messages) != 2 || body.Messages[0].Content != "hello" {
		t.Fatalf("transcript = %+v", body.Messages)
	}
}

// TestWebUISessionValidatesUser: this value becomes half of a session key that
// names a file, so it goes through the same validation the chat route applies.
// A traversal here would read a file outside the sessions directory.
func TestWebUISessionValidatesUser(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	for _, bad := range []string{"../etc/passwd", "a:b", strings.Repeat("x", MaxUserLength+1), "has space"} {
		w := do(t, s, http.MethodGet, webuiPathSession+"?user="+url.QueryEscape(bad), "secret", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("session user=%q: status %d, want 400", bad, w.Code)
		}
	}
}

// TestWebUISessionSurvivesAMissingTranscript: a fresh browser key names a
// session that does not exist yet. That is the normal first load, and it must
// render an empty log rather than an error.
func TestWebUISessionSurvivesAMissingTranscript(t *testing.T) {
	s, err := New(&fakeAgent{reply: "hi"}, Options{
		Listen:     "127.0.0.1:0",
		APIKeys:    []string{"secret"},
		WebUI:      true,
		Transcript: func(string) ([]TranscriptMessage, error) { return nil, errors.New("no such session") },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := do(t, s, http.MethodGet, webuiPathSession+"?user=web-00000000", "secret", "")
	if w.Code != http.StatusOK {
		t.Fatalf("session: status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"messages":[]`) {
		t.Errorf("body = %s, want an empty message list", w.Body.String())
	}
}

// TestWebUIIndexIsNotACatchAll: "/" on a ServeMux matches everything. An unknown
// path must 404, not answer 200 with HTML a JSON client will fail to parse.
func TestWebUIIndexIsNotACatchAll(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/nope", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /v1/nope: status %d, want 404", w.Code)
	}
}

// TestWebUIDisabledIgnoresCookies: with the UI off, a cookie session must not
// authenticate anything — the flag is the gate, not the presence of a store.
// The session is injected directly, so this fails for the right reason rather
// than because a second server happens to hold a different map.
func TestWebUIDisabledIgnoresCookies(t *testing.T) {
	off := testServer(t, &fakeAgent{reply: "hi"})
	id, _, err := off.webuiSessions.create(time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.AddCookie(&http.Cookie{Name: webuiCookieName, Value: id})
	w := httptest.NewRecorder()
	off.routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("cookie against a webui-disabled server: status %d, want 401", w.Code)
	}
}

// TestWebUILoginFailuresGoThroughTheRejectionLimiter: the login route is the
// easiest thing on the server to spray, so it must share the throttle rather
// than log a line per attempt into whatever file the operator redirects to.
func TestWebUILoginFailuresGoThroughTheRejectionLimiter(t *testing.T) {
	s := webuiServer(t, &fakeAgent{reply: "hi"})
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest(http.MethodPost, webuiPathLogin, strings.NewReader(`{"key":"wrong"}`))
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
	}
	s.rejections.mu.Lock()
	defer s.rejections.mu.Unlock()
	if s.rejections.last.IsZero() {
		t.Fatal("login failures never reached the rejection limiter")
	}
	// The first is logged and the rest are counted, so four are pending.
	if s.rejections.since != 4 {
		t.Errorf("limiter counted %d suppressed rejections, want 4", s.rejections.since)
	}
}
