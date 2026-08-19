package api

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/log"
)

// webuiFS holds the browser UI. The assets are embedded rather than read from
// disk for the same reason the bundled skills are (internal/skills/bundled.go):
// a relative path resolves against the process working directory, so a served
// directory would work only when joshbot is run from a checkout of its own
// source tree. The consequence is the usual one — these are embed paths, not
// filesystem paths, and editing an asset needs a rebuild to take effect.
//
//go:embed webui/index.html webui/static
var webuiFS embed.FS

// WebUI routes are served only when api.webui is true. When it is false they are
// not registered at all, so they 404 as if the feature did not exist.
//
// The default is false and that is a security decision, not timidity: this page
// is an HTML form that accepts a key granting shell and filesystem access
// through the agent. Turning it on at upgrade for every existing `joshbot serve`
// bind — including the ones on 0.0.0.0 behind someone's reverse proxy — would
// publish a login form nobody asked for. Serving it is an explicit act.
const (
	webuiPathIndex   = "/"
	webuiPathStatic  = "/webui/static/"
	webuiPathLogin   = "/webui/login"
	webuiPathLogout  = "/webui/logout"
	webuiPathConfig  = "/webui/config"
	webuiPathSession = "/webui/session"
)

// webuiCookieName holds the opaque browser session id. It is httpOnly, so the
// page's own JavaScript cannot read it — an XSS in the transcript renderer
// therefore cannot exfiltrate a usable credential, only act within the page.
const webuiCookieName = "joshbot_webui"

// csrfHeader is the double-submit header the cookie path requires. A
// cross-origin form post cannot set a custom header, which is the whole
// mechanism; SameSite=Strict is the belt and this is the braces.
const csrfHeader = "X-Joshbot-CSRF"

// webuiSessionTTL bounds a browser session. It is short enough that a stolen
// cookie expires on its own and long enough that an operator is not re-typing a
// key all day. Sliding: activity extends it, idleness does not.
const webuiSessionTTL = 12 * time.Hour

// maxWebUISessions caps the in-memory session map. The map is keyed by a value
// the server mints, so only a caller who can authenticate can add to it — but
// "authenticated" is not "unlimited", and a login loop would otherwise grow the
// map without bound for the process's lifetime. At the cap the oldest entry is
// evicted, which costs that browser a re-login and costs the process nothing.
const maxWebUISessions = 64

// TranscriptReader returns the stored transcript for an API session user, so a
// reload can render what was already said.
//
// It is read-only by construction: there is no write side and no delete side on
// the HTTP surface. "New conversation" in the browser rotates the user half of
// the session key and leaves the old session on disk, because a browser button
// that silently deletes an operator's transcript is not a feature — `joshbot
// sessions prune` is where deletion lives, in front of an operator who can see
// what they are removing.
type TranscriptReader func(user string) ([]TranscriptMessage, error)

// TranscriptMessage is one stored turn, flattened to what a browser can render.
// Tool calls and images are deliberately not carried: the UI shows neither, and
// shipping them would be an unused attack surface on untrusted stored text.
type TranscriptMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// webuiSession is one logged-in browser.
type webuiSession struct {
	csrf    string
	expires time.Time
	created time.Time
}

// webuiSessions is the in-memory session store. It is deliberately not
// persisted: a restart signing every browser out is correct behaviour for a
// credential that grants shell access, and a persisted one would be a second
// on-disk secret to protect.
type webuiSessions struct {
	mu sync.Mutex
	m  map[string]*webuiSession
}

func newWebUISessions() *webuiSessions {
	return &webuiSessions{m: make(map[string]*webuiSession)}
}

// create mints a session id and its CSRF token, both from crypto/rand.
func (ws *webuiSessions) create(now time.Time) (id, csrf string, err error) {
	if id, err = randomToken(); err != nil {
		return "", "", err
	}
	if csrf, err = randomToken(); err != nil {
		return "", "", err
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.sweepLocked(now)
	ws.evictOldestLocked()
	ws.m[id] = &webuiSession{csrf: csrf, expires: now.Add(webuiSessionTTL), created: now}
	return id, csrf, nil
}

// get returns a live session and slides its expiry. An expired entry is removed
// rather than returned: expiry that only hides an entry is not expiry.
func (ws *webuiSessions) get(id string, now time.Time) (*webuiSession, bool) {
	if id == "" {
		return nil, false
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	s, ok := ws.m[id]
	if !ok {
		return nil, false
	}
	if !now.Before(s.expires) {
		delete(ws.m, id)
		return nil, false
	}
	s.expires = now.Add(webuiSessionTTL)
	return s, true
}

func (ws *webuiSessions) drop(id string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	delete(ws.m, id)
}

func (ws *webuiSessions) len() int {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return len(ws.m)
}

func (ws *webuiSessions) sweepLocked(now time.Time) {
	for id, s := range ws.m {
		if !now.Before(s.expires) {
			delete(ws.m, id)
		}
	}
}

func (ws *webuiSessions) evictOldestLocked() {
	for len(ws.m) >= maxWebUISessions {
		var oldestID string
		var oldest time.Time
		for id, s := range ws.m {
			if oldestID == "" || s.created.Before(oldest) {
				oldestID, oldest = id, s.created
			}
		}
		if oldestID == "" {
			return
		}
		delete(ws.m, oldestID)
	}
}

// randomToken returns 32 bytes of crypto/rand, URL-safe base64 encoded.
func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// registerWebUI adds the browser routes. It is called only when api.webui is on.
func (s *Server) registerWebUI(mux *http.ServeMux) {
	assets, err := fs.Sub(webuiFS, "webui/static")
	if err != nil {
		// Only reachable if the embed directive and this path disagree, which
		// is a build-time mistake, not a runtime condition.
		log.Error("WebUI assets unavailable", "error", err)
		return
	}
	fileServer := http.StripPrefix(webuiPathStatic, http.FileServer(http.FS(assets)))

	mux.HandleFunc(webuiPathIndex, func(w http.ResponseWriter, r *http.Request) {
		// "/" on a ServeMux is the catch-all. Anything that is not the document
		// is a 404, not a silent redirect to the app, so a mistyped API path
		// does not answer 200 with HTML that a client will try to parse as JSON.
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "", "Method not allowed; use GET.")
			return
		}
		doc, err := webuiFS.ReadFile("webui/index.html")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api_error", "", "The web UI document is unavailable.")
			return
		}
		webuiHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The document itself carries no secret and no session state, so it is
		// cacheable — but only privately, never by a shared proxy.
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(doc)
	})

	mux.HandleFunc(webuiPathStatic, func(w http.ResponseWriter, r *http.Request) {
		webuiHeaders(w)
		fileServer.ServeHTTP(w, r)
	})

	mux.HandleFunc(webuiPathLogin, s.handleWebUILogin)
	mux.HandleFunc(webuiPathLogout, s.handleWebUILogout)
	mux.HandleFunc(webuiPathConfig, s.requireAuth(s.handleWebUIConfig))
	mux.HandleFunc(webuiPathSession, s.requireAuth(s.handleWebUISession))
}

// webuiHeaders sets the response headers that bound what the page may do.
//
// The CSP is the load-bearing one: 'none' by default with 'self' for script and
// style and no 'unsafe-inline' at all, which is why index.html carries no inline
// <script> or <style> and app.js attaches every handler in code. connect-src
// 'self' means the page cannot exfiltrate a transcript to another origin even if
// model output managed to inject script; frame-ancestors 'none' stops
// clickjacking a form that authenticates shell access.
func webuiHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy",
		"default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
			"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	// no-referrer keeps the origin out of any outbound navigation the operator
	// makes from a link in model output.
	h.Set("Referrer-Policy", "no-referrer")
}

type webuiLoginRequest struct {
	Key string `json:"key"`
}

// handleWebUILogin exchanges an api.api_keys value for a cookie.
//
// A browser page cannot ship a bearer key against a fail-closed server, and the
// alternatives were both refused: `?key=` leaks into history, proxy logs and
// Referer, and a loopback exemption disables authentication for every process on
// the host, not just the operator's browser. So the key is presented once, over
// a POST body, and exchanged for something the page itself cannot read.
//
// The validation is the *same* keyMatches path the bearer header uses — same
// constant-time compare, same key list — and a failure goes through the same
// rejection limiter, so a spray against this route is throttled in the log
// exactly like a spray against /v1.
func (s *Server) handleWebUILogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "", "Method not allowed; use POST.")
		return
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_request_error", "", "Cross-origin request refused.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req webuiLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", "Could not parse request body.")
		return
	}

	if key := strings.TrimSpace(req.Key); key == "" || !s.keyMatches([]byte(key)) {
		if n, ok := s.rejections.note(); ok {
			log.Warn("WebUI login rejected", "remote", remoteHost(r), "since_last_log", n)
		}
		writeError(w, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key",
			"That key was not accepted. Use a value from api.api_keys.")
		return
	}

	id, csrf, err := s.webuiSessions.create(time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error", "", "Could not start a session.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:  webuiCookieName,
		Value: id,
		Path:  "/",
		// The page never reads this, only sends it. httpOnly is what keeps an
		// XSS from turning into a stolen credential.
		HttpOnly: true,
		// Strict, not Lax: there is no cross-site entry point that should carry
		// this cookie, and Lax would send it on a top-level navigation from
		// anywhere.
		SameSite: http.SameSiteStrictMode,
		// Secure only under TLS. Setting it unconditionally would make the
		// cookie undeliverable on the plain-HTTP loopback bind that is the
		// normal case, and the browser would silently drop it — a login that
		// appears to succeed and then 401s forever.
		Secure:  isTLS(r),
		Expires: time.Now().Add(webuiSessionTTL),
		MaxAge:  int(webuiSessionTTL / time.Second),
	})
	writeJSON(w, http.StatusOK, map[string]string{"csrf": csrf})
}

func (s *Server) handleWebUILogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "", "Method not allowed; use POST.")
		return
	}
	if c, err := r.Cookie(webuiCookieName); err == nil {
		s.webuiSessions.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     webuiCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isTLS(r),
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// webuiConfigResponse is the non-secret set of facts the page needs to render
// honestly. Every field here is something the page would otherwise have to
// invent — which is exactly what the mockup's hardcoded version badge did.
type webuiConfigResponse struct {
	Model   string `json:"model"`
	Version string `json:"version"`
	CSRF    string `json:"csrf,omitempty"`
}

func (s *Server) handleWebUIConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "", "Method not allowed; use GET.")
		return
	}
	resp := webuiConfigResponse{Model: ModelID, Version: s.version}
	// The CSRF token is served only to the cookie path, because only that path
	// requires it. A bearer client sees no token and needs none.
	if sess, ok := s.webuiSessionFor(r); ok {
		resp.CSRF = sess.csrf
	}
	webuiHeaders(w)
	writeJSON(w, http.StatusOK, resp)
}

// handleWebUISession returns the stored transcript for one API session, so a
// page reload renders the conversation instead of an empty log.
func (s *Server) handleWebUISession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "", "Method not allowed; use GET.")
		return
	}
	// The same validation the chat route applies, for the same reason: this
	// value is caller-supplied and becomes half of a session key that names a
	// file on disk.
	user, err := senderID(r.URL.Query().Get("user"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", err.Error())
		return
	}
	if s.transcript == nil {
		writeJSON(w, http.StatusOK, map[string]any{"messages": []TranscriptMessage{}})
		return
	}
	msgs, err := s.transcript(user)
	if err != nil {
		// A session that has never been written is not an error worth surfacing
		// as one: a fresh browser key names a session that does not exist yet,
		// which is the common case on first load.
		log.Debug("WebUI transcript unavailable", "error", err)
		msgs = nil
	}
	if msgs == nil {
		msgs = []TranscriptMessage{}
	}
	webuiHeaders(w)
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

// webuiSessionFor resolves the cookie session on a request, or reports false.
// It returns false whenever the WebUI is off, so no cookie is ever honoured on a
// server that does not serve the page.
func (s *Server) webuiSessionFor(r *http.Request) (*webuiSession, bool) {
	if !s.webui || s.webuiSessions == nil {
		return nil, false
	}
	c, err := r.Cookie(webuiCookieName)
	if err != nil {
		return nil, false
	}
	return s.webuiSessions.get(c.Value, time.Now())
}

// cookieAuthOK reports whether a request authenticates via the browser cookie.
//
// Two conditions beyond a live session, and both are load-bearing. A non-GET
// carries a CSRF token matching the session's, constant-time compared: without
// it, any page the operator visits could POST a chat turn — which reaches the
// shell tool — from their authenticated browser. And the request is same-origin,
// which catches the simple form post before the token check does.
func (s *Server) cookieAuthOK(r *http.Request) bool {
	sess, ok := s.webuiSessionFor(r)
	if !ok {
		return false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if !sameOrigin(r) {
		return false
	}
	presented := r.Header.Get(csrfHeader)
	return subtle.ConstantTimeCompare([]byte(sess.csrf), []byte(presented)) == 1
}

// sameOrigin checks the Origin header against the request's own host.
//
// A request with no Origin is allowed: non-browser clients omit it, and this
// check exists to constrain browsers, which always send it on a cross-origin
// POST. The CSRF token is the primary defence; this is the cheap one in front.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// isTLS reports whether the request arrived over TLS, including through a proxy
// that terminated it. X-Forwarded-Proto is trusted only to *add* the Secure
// flag, never to remove it or to authenticate anything, so a spoofed header
// costs an attacker nothing they could gain.
func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
