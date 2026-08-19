package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/redact"
)

// MaxRequestBytes bounds a request body. An agent turn is a chat message, not
// an upload, and an unbounded body is a trivial memory-exhaustion vector on a
// server whose whole job is to hold a conversation in memory.
const MaxRequestBytes = 1 << 20 // 1 MiB

// ChannelName is the channel half of the session key for API callers. joshbot
// keys sessions as "channel:senderID", so this keeps API conversations in their
// own namespace, distinct from cli, telegram and discord.
const ChannelName = "api"

// DefaultUser is the sender identity used when a request omits `user`. Every
// anonymous caller therefore shares one conversation, which is the right
// default for a single-operator deployment and is why `user` exists for anyone
// who wants separation.
const DefaultUser = "default"

// Processor is the agent behind the endpoint. It is an interface so the handlers
// can be tested without constructing a provider, a session store and a tool
// registry.
type Processor interface {
	Process(ctx context.Context, msg bus.InboundMessage) (string, error)
}

// ErrNoAPIKey reports a server configured with no credential. It is fatal at
// construction rather than a warning at runtime: this endpoint reaches the shell
// and filesystem tools, so serving it unauthenticated is remote code execution,
// not a relaxed default.
var ErrNoAPIKey = errors.New("no API key configured: set api.api_keys in config.json (or JOSHBOT_API__API_KEYS); joshbot serve has no unauthenticated mode")

// Server is the OpenAI-compatible HTTP server.
type Server struct {
	agent Processor
	// transcriber is the operator's configured speech-to-text callback, or nil
	// when `stt` is unset. Nil is meaningful: /v1/audio/transcriptions answers
	// 501 naming the config key rather than pretending to work.
	transcriber Transcriber
	keys        [][]byte
	http        *http.Server
	// served latches the one run this Server gets. http.Server cannot be
	// restarted after Shutdown: a second Serve would bind the port, take
	// ErrServerClosed straight back, and — since that error is mapped to nil —
	// report a clean successful run while serving nothing. A SIGHUP reload or a
	// supervisor loop would then no-op silently, which is the same class of bug
	// the bus, Discord and mcp.Client each shipped once.
	served atomic.Bool

	// rejections throttles the 401 log line. Unauthenticated requests are the
	// one thing an attacker can generate without a credential, and a line per
	// request turns that into a disk-fill primitive on any install that
	// redirects the log to a file.
	rejections limiter
}

// limiter allows one event per window and counts what it dropped, so a burst
// costs one line rather than one line per request and the line still says how
// many there were. Silently dropping would be worse than the flood: a password
// spray would look like a single stray request.
type limiter struct {
	mu    sync.Mutex
	last  time.Time
	since int
}

// rejectionLogWindow is the minimum gap between 401 log lines.
const rejectionLogWindow = time.Minute

// note records an event and reports whether it should be logged, along with how
// many events (including this one) the line covers.
func (l *limiter) note() (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.since++
	if !l.last.IsZero() && time.Since(l.last) < rejectionLogWindow {
		return 0, false
	}
	l.last = time.Now()
	n := l.since
	l.since = 0
	return n, true
}

// ErrServerReused reports a second Serve on a Server that has already run.
var ErrServerReused = errors.New("api: server already served; construct a new one")

// Transcriber turns audio bytes into text. It is the same callback the Telegram
// channel uses for voice notes, so the endpoint and the chat share one
// credential and one configured model.
type Transcriber func(ctx context.Context, audio []byte, filename string) (string, error)

// Options configures a Server.
type Options struct {
	// Listen is the bind address, host:port.
	Listen string
	// APIKeys are the accepted bearer credentials. At least one non-empty key
	// is required.
	APIKeys []string
	// Transcriber enables POST /v1/audio/transcriptions. Nil disables the
	// route's work but not the route: it answers 501 naming the config key,
	// which is more useful to a client than a 404.
	Transcriber Transcriber
}

// New builds a Server. It fails when no usable API key is configured.
func New(a Processor, opts Options) (*Server, error) {
	if a == nil {
		return nil, errors.New("api: agent is required")
	}

	keys := make([][]byte, 0, len(opts.APIKeys))
	for _, k := range opts.APIKeys {
		// A blank entry is a config typo, not a wildcard. Skipping it here is
		// what makes a config of only blanks fail closed below.
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, []byte(k))
		}
	}
	if len(keys) == 0 {
		return nil, ErrNoAPIKey
	}

	// No default is chosen here. The caller resolves it from
	// config.DefaultAPIListen, and a second copy of that address in this package
	// would be a value nothing keeps in agreement with the first — a drift shows
	// up as a server not on the port the docs name.
	if strings.TrimSpace(opts.Listen) == "" {
		return nil, errors.New("api: listen address is required")
	}

	s := &Server{agent: a, transcriber: opts.Transcriber, keys: keys}
	s.http = &http.Server{
		Addr:    opts.Listen,
		Handler: s.routes(),
		// No WriteTimeout: a streamed answer legitimately outlives any fixed
		// deadline, and the agent applies its own timeout to the work itself.
		// ReadHeaderTimeout still closes a client that connects and says
		// nothing.
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout bounds the whole request, headers and body. It is safe to
		// set here and not on the write side: a request is a chat message capped
		// at MaxRequestBytes, so nothing legitimately dribbles one in for
		// minutes, while a streamed *answer* legitimately outlives any deadline.
		// Without it an authenticated client can hold a goroutine open
		// indefinitely by sending one byte of body at a time.
		ReadTimeout: 60 * time.Second,
		IdleTimeout: 60 * time.Second,
	}
	return s, nil
}

// Addr returns the configured bind address.
func (s *Server) Addr() string { return s.http.Addr }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.requireAuth(s.handleChatCompletions))
	mux.HandleFunc("/v1/models", s.requireAuth(s.handleModels))
	mux.HandleFunc("/v1/audio/transcriptions", s.requireAuth(s.handleTranscriptions))
	// Health is deliberately unauthenticated and returns no information about
	// the configuration — it exists so a process supervisor or container
	// healthcheck does not need a credential.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

// Serve runs the server until ctx is cancelled, then shuts it down gracefully.
func (s *Server) Serve(ctx context.Context) error {
	if !s.served.CompareAndSwap(false, true) {
		return ErrServerReused
	}
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("api: listen on %s: %w", s.http.Addr, err)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	}
}

// requireAuth wraps a handler with bearer-token authentication.
//
// The comparison is constant-time and every configured key is checked even
// after a match, so the time taken does not reveal which key matched or how
// many are configured. The credential itself is never logged or echoed: a 401
// body says only that the key is missing or invalid.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := bearerToken(r.Header.Get("Authorization"))
		if presented == "" || !s.keyMatches([]byte(presented)) {
			if n, ok := s.rejections.note(); ok {
				log.Warn("API request rejected", "path", r.URL.Path, "remote", remoteHost(r),
					"since_last_log", n)
			}
			writeError(w, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key",
				"Incorrect API key provided. Send it as 'Authorization: Bearer <key>'.")
			return
		}
		next(w, r)
	}
}

func (s *Server) keyMatches(presented []byte) bool {
	matched := 0
	for _, k := range s.keys {
		matched |= subtle.ConstantTimeCompare(k, presented)
	}
	return matched == 1
}

// bearerToken pulls the credential out of an Authorization header. The scheme
// match is case-insensitive because RFC 7235 says it is, and clients differ.
func bearerToken(header string) string {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// remoteHost strips the port so a rejection log records who, not which ephemeral
// socket.
func remoteHost(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "", "Method not allowed; use GET.")
		return
	}
	writeJSON(w, http.StatusOK, modelsResponse{
		Object: "list",
		Data: []modelInfo{{
			ID:      ModelID,
			Object:  "model",
			Created: 0,
			OwnedBy: "joshbot",
		}},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Debug("API response write failed", "error", err)
	}
}

// writeError answers with an error joshbot itself wrote. The message is sent
// verbatim: it is a constant in this package, so it carries no credential, and
// running it through the redactor mangles it. The 401 body said
// "Authorization: Bearer [REDACTED]" for exactly that reason (#238) — the
// header rule ate the <key> placeholder that tells the caller what to send.
//
// Anything whose text came from outside this package — a provider error, an
// upstream failure — goes through writeUpstreamError instead.
func writeError(w http.ResponseWriter, status int, kind, code, msg string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Message: msg, Type: kind, Code: code}})
}

// writeUpstreamError answers with an error that originated outside joshbot,
// redacting it first. See safeErrorMessage.
func writeUpstreamError(w http.ResponseWriter, status int, kind, code, msg string) {
	writeError(w, status, kind, code, safeErrorMessage(msg))
}

// safeErrorMessage strips credentials and the operator's home path out of an
// error before it crosses the network.
//
// The messages it is applied to are not joshbot's own prose: a 502 carries the
// provider's error text verbatim, and providers routinely echo credentials and
// absolute paths. An API caller is authenticated but is not the operator, so
// handing it the operator's upstream provider key is a privilege escalation.
//
// The cover is real but partial, and the limit is worth stating rather than
// assuming away: redact.String catches assignment shapes (api_key=..., "token":
// "...") and Authorization headers, and redact.HomePath rewrites the running
// process's own $HOME. A credential quoted in free prose — OpenAI's own
// "Incorrect API key provided: sk-..." is the canonical case — is NOT caught.
// Widening that belongs in internal/redact, where every other sink benefits,
// not in a second pattern list here.
//
// This happens at construction rather than through redact.Writer, which only
// wraps the log sink and never sees an HTTP body.
func safeErrorMessage(msg string) string {
	return redact.HomePath(redact.String(msg))
}

// newID returns an OpenAI-shaped completion id. It uses crypto/rand so ids are
// not guessable across concurrent callers; a failure falls back to a fixed
// suffix rather than aborting a request, since the id is an identifier, not a
// credential.
func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "chatcmpl-joshbot"
	}
	return "chatcmpl-" + hex.EncodeToString(b[:])
}
