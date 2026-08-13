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
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/log"
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
	keys  [][]byte
	http  *http.Server
}

// Options configures a Server.
type Options struct {
	// Listen is the bind address, host:port.
	Listen string
	// APIKeys are the accepted bearer credentials. At least one non-empty key
	// is required.
	APIKeys []string
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

	listen := opts.Listen
	if listen == "" {
		listen = "127.0.0.1:18791"
	}

	s := &Server{agent: a, keys: keys}
	s.http = &http.Server{
		Addr:    listen,
		Handler: s.routes(),
		// No WriteTimeout: a streamed answer legitimately outlives any fixed
		// deadline, and the agent applies its own timeout to the work itself.
		// ReadHeaderTimeout still closes a client that connects and says
		// nothing.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

// Addr returns the configured bind address.
func (s *Server) Addr() string { return s.http.Addr }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.requireAuth(s.handleChatCompletions))
	mux.HandleFunc("/v1/models", s.requireAuth(s.handleModels))
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
			log.Warn("API request rejected", "path", r.URL.Path, "remote", remoteHost(r))
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

func writeError(w http.ResponseWriter, status int, kind, code, msg string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Message: msg, Type: kind, Code: code}})
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
