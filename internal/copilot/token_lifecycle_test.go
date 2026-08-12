package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// countingTokenServer stands in for api.github.com/copilot_internal/v2/token.
// It counts exchanges and lets the caller decide what each one returns, which
// is how the caching rules in ensureAPIToken are observed at all: the only
// visible effect of a cache hit is an exchange that did not happen.
type countingTokenServer struct {
	exchanges int
	authSeen  []string
}

func stubTokenServer(t *testing.T, issue func(n int) (token string, expiresAt int64)) *countingTokenServer {
	t.Helper()
	s := &countingTokenServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.exchanges++
		s.authSeen = append(s.authSeen, r.Header.Get("Authorization"))
		tok, exp := issue(s.exchanges)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_at": exp})
	}))
	t.Cleanup(srv.Close)

	old := CopilotTokenURL
	CopilotTokenURL = srv.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	return s
}

// stubChatServer points CopilotAPIURL at a test server driven by handle.
func stubChatServer(t *testing.T, handle http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handle)
	t.Cleanup(srv.Close)
	old := CopilotAPIURL
	CopilotAPIURL = srv.URL
	t.Cleanup(func() { CopilotAPIURL = old })
}

func okChatBody(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "c1",
		"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "hi"}}},
	})
}

func chatOnce(t *testing.T, p *CopilotProvider) error {
	t.Helper()
	_, err := p.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{{Role: providers.RoleUser, Content: "x"}},
	})
	return err
}

// ensureAPIToken must re-exchange once the cached token is inside the last
// minute of its life. Serving a token that is still technically valid but about
// to expire is how a long-running gateway starts 401-ing mid-conversation.
func TestEnsureAPIToken_RefreshesInsideTheLastMinuteOfValidity(t *testing.T) {
	// Expires in 30s: still in the future, but inside the 60s guard band.
	srv := stubTokenServer(t, func(n int) (string, int64) {
		if n == 1 {
			return "about-to-expire", time.Now().Add(30 * time.Second).Unix()
		}
		return "fresh", time.Now().Add(2 * time.Hour).Unix()
	})

	var authSeen []string
	stubChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		authSeen = append(authSeen, r.Header.Get("Authorization"))
		okChatBody(w)
	})

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "gho_x")
	if err := chatOnce(t, p); err != nil {
		t.Fatalf("first Chat: %v", err)
	}
	if err := chatOnce(t, p); err != nil {
		t.Fatalf("second Chat: %v", err)
	}

	if srv.exchanges != 2 {
		t.Fatalf("token exchanges = %d, want 2 — a token expiring in 30s must be refreshed, not replayed", srv.exchanges)
	}
	if len(authSeen) != 2 || authSeen[1] != "Bearer fresh" {
		t.Fatalf("second request Authorization = %v, want the refreshed token", authSeen)
	}
}

// A token comfortably inside its validity window is reused. This is the other
// half of the guard band: widening it far enough would re-exchange on every
// single turn, adding a round trip to every message.
func TestEnsureAPIToken_ReusesTokenWellInsideValidity(t *testing.T) {
	srv := stubTokenServer(t, func(int) (string, int64) {
		return "long-lived", time.Now().Add(30 * time.Minute).Unix()
	})
	stubChatServer(t, func(w http.ResponseWriter, r *http.Request) { okChatBody(w) })

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "gho_x")
	for i := 0; i < 3; i++ {
		if err := chatOnce(t, p); err != nil {
			t.Fatalf("Chat %d: %v", i, err)
		}
	}
	if srv.exchanges != 1 {
		t.Fatalf("token exchanges = %d, want 1 — a token valid for 30 minutes must not be re-exchanged", srv.exchanges)
	}
}

// A rejected credential may just be a stale cached exchange. The cache must be
// dropped so the next call re-exchanges instead of replaying a dead token
// forever — otherwise a single 401 wedges the provider for the process's life.
func TestCopilotProvider_Chat_RejectedStatusClearsCachedToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"unauthorized", http.StatusUnauthorized},
		{"forbidden", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := stubTokenServer(t, func(n int) (string, int64) {
				return "tok" + string(rune('0'+n)), 0
			})

			call := 0
			stubChatServer(t, func(w http.ResponseWriter, r *http.Request) {
				call++
				if call == 2 {
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "bad credentials"}})
					return
				}
				okChatBody(w)
			})

			p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "gho_x")
			if err := chatOnce(t, p); err != nil {
				t.Fatalf("first Chat: %v", err)
			}
			if srv.exchanges != 1 {
				t.Fatalf("exchanges after first Chat = %d, want 1", srv.exchanges)
			}
			if err := chatOnce(t, p); err == nil {
				t.Fatalf("Chat returned nil error for status %d", tc.status)
			}
			// The rejection itself must not re-exchange; only the next call does.
			if srv.exchanges != 1 {
				t.Fatalf("exchanges after rejection = %d, want 1", srv.exchanges)
			}
			if err := chatOnce(t, p); err != nil {
				t.Fatalf("third Chat: %v", err)
			}
			if srv.exchanges != 2 {
				t.Fatalf("exchanges after recovery = %d, want 2 — a %d must invalidate the cached Copilot token", srv.exchanges, tc.status)
			}
		})
	}
}

// A non-auth failure (500) says nothing about the credential, so the cached
// token must survive it. Clearing on every error would turn a provider blip
// into an extra token exchange per retry.
func TestCopilotProvider_Chat_ServerErrorKeepsCachedToken(t *testing.T) {
	srv := stubTokenServer(t, func(int) (string, int64) { return "tok", 0 })

	call := 0
	stubChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("upstream boom"))
			return
		}
		okChatBody(w)
	})

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "gho_x")
	if err := chatOnce(t, p); err != nil {
		t.Fatalf("first Chat: %v", err)
	}
	if err := chatOnce(t, p); err == nil {
		t.Fatal("expected an error for status 500")
	}
	if err := chatOnce(t, p); err != nil {
		t.Fatalf("third Chat: %v", err)
	}
	if srv.exchanges != 1 {
		t.Fatalf("token exchanges = %d, want 1 — a 500 is not a credential problem", srv.exchanges)
	}
}

// The exchange authenticates with GitHub's "token" scheme. Sending Bearer here
// (the scheme the *result* is used with) is rejected by api.github.com.
func TestExchangeCopilotToken_UsesTokenSchemeAgainstGitHub(t *testing.T) {
	srv := stubTokenServer(t, func(int) (string, int64) { return "copilot-tok", 0 })

	tok, err := ExchangeCopilotToken(context.Background(), "gho_secret")
	if err != nil {
		t.Fatalf("ExchangeCopilotToken: %v", err)
	}
	if tok.Token != "copilot-tok" {
		t.Errorf("token = %q, want copilot-tok", tok.Token)
	}
	if len(srv.authSeen) != 1 || srv.authSeen[0] != "token gho_secret" {
		t.Errorf("Authorization = %v, want [\"token gho_secret\"]", srv.authSeen)
	}
}

func TestExchangeCopilotToken_ErrorPaths(t *testing.T) {
	t.Run("no github token", func(t *testing.T) {
		_, err := ExchangeCopilotToken(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "joshbot auth github-copilot") {
			t.Fatalf("err = %v, want it to name the re-auth command", err)
		}
	})

	t.Run("non-auth failure status", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("gateway down"))
		}))
		defer s.Close()
		old := CopilotTokenURL
		CopilotTokenURL = s.URL
		defer func() { CopilotTokenURL = old }()

		_, err := ExchangeCopilotToken(context.Background(), "gho_x")
		if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "gateway down") {
			t.Fatalf("err = %v, want it to carry the status and body", err)
		}
	})

	t.Run("unparseable body", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>not json</html>"))
		}))
		defer s.Close()
		old := CopilotTokenURL
		CopilotTokenURL = s.URL
		defer func() { CopilotTokenURL = old }()

		if _, err := ExchangeCopilotToken(context.Background(), "gho_x"); err == nil {
			t.Fatal("expected a parse error")
		}
	})

	// A 200 with no token is the one failure that would otherwise sail through
	// and produce "Authorization: Bearer " on every subsequent API call.
	t.Run("ok status but empty token", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"expires_at": 123})
		}))
		defer s.Close()
		old := CopilotTokenURL
		CopilotTokenURL = s.URL
		defer func() { CopilotTokenURL = old }()

		_, err := ExchangeCopilotToken(context.Background(), "gho_x")
		if err == nil || !strings.Contains(err.Error(), "no token") {
			t.Fatalf("err = %v, want an explicit empty-token error", err)
		}
	})

	t.Run("unreachable host", func(t *testing.T) {
		old := CopilotTokenURL
		CopilotTokenURL = "http://127.0.0.1:1/copilot_internal/v2/token"
		defer func() { CopilotTokenURL = old }()

		if _, err := ExchangeCopilotToken(context.Background(), "gho_x"); err == nil {
			t.Fatal("expected a transport error")
		}
	})
}

// ChatStream must report ErrStreamingUnsupported by wrapping the sentinel, not
// as a bare error: internal/agent falls back to Chat on errors.Is, and anything
// else kills every interactive turn on this provider.
func TestCopilotProvider_ChatStream_WrapsSentinel(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "gho_x")
	ch, err := p.ChatStream(context.Background(), providers.ChatRequest{})
	if ch != nil {
		t.Error("ChatStream returned a non-nil channel alongside an error")
	}
	if !errors.Is(err, providers.ErrStreamingUnsupported) {
		t.Fatalf("err = %v, want it to wrap providers.ErrStreamingUnsupported", err)
	}
}

// --- device flow ---

// stubDeviceFlow points the two GitHub OAuth endpoints at test servers.
func stubDeviceFlow(t *testing.T, device http.HandlerFunc, token http.HandlerFunc) {
	t.Helper()
	d := httptest.NewServer(device)
	tk := httptest.NewServer(token)
	t.Cleanup(d.Close)
	t.Cleanup(tk.Close)

	oldD, oldT := DeviceCodeURL, AccessTokenURL
	DeviceCodeURL, AccessTokenURL = d.URL, tk.URL
	t.Cleanup(func() { DeviceCodeURL, AccessTokenURL = oldD, oldT })
}

// RunDeviceFlow must persist the OAuth token where LoadToken will find it, and
// the token it writes must survive an immediate LoadToken: the zero expires_in
// GitHub returns for an OAuth App must not be stamped as an expiry.
func TestRunDeviceFlow_PersistsATokenLoadTokenAccepts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stubDeviceFlow(t,
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dev-code",
				"user_code":        "ABCD-1234",
				"verification_uri": "https://github.com/login/device",
				"interval":         5,
				"expires_in":       900,
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			// Authorized on the very first attempt, so no ticker wait.
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "gho_persisted"})
		},
	)

	got, err := RunDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("RunDeviceFlow: %v", err)
	}
	if got.AccessToken != "gho_persisted" {
		t.Fatalf("access token = %q", got.AccessToken)
	}

	loaded, err := LoadToken(home)
	if err != nil {
		t.Fatalf("LoadToken after RunDeviceFlow: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadToken returned nil — RunDeviceFlow did not persist the token")
	}
	if loaded.AccessToken != "gho_persisted" {
		t.Fatalf("persisted token = %q, want gho_persisted", loaded.AccessToken)
	}
	// GitHub's OAuth-App device flow returns no expires_in. Stamping the token
	// with now+0 marks it dead on arrival, and LoadToken then rejects it on the
	// very next read.
	if loaded.ExpiresAt != 0 {
		t.Fatalf("ExpiresAt = %d, want 0 — a missing expires_in means no expiry, not expired now", loaded.ExpiresAt)
	}
	if _, err := os.Stat(filepath.Join(home, ".joshbot", "auth.json")); err != nil {
		t.Fatalf("auth.json not at the path AuthFilePath advertises: %v", err)
	}
}

func TestRunDeviceFlow_FailurePropagates(t *testing.T) {
	t.Run("device code request fails", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		stubDeviceFlow(t,
			func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
			func(w http.ResponseWriter, r *http.Request) { t.Error("token endpoint must not be reached") },
		)
		if _, err := RunDeviceFlow(context.Background()); err == nil {
			t.Fatal("expected an error when device code initiation fails")
		}
	})

	t.Run("user denies authorization", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		stubDeviceFlow(t,
			func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "d", "user_code": "u", "interval": 5})
			},
			func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "access_denied"})
			},
		)
		if _, err := RunDeviceFlow(context.Background()); err == nil {
			t.Fatal("expected an error when the user denies authorization")
		}
		// Nothing may be written on a failed flow.
		if _, err := os.Stat(filepath.Join(home, ".joshbot", "auth.json")); !os.IsNotExist(err) {
			t.Fatalf("auth.json exists after a denied flow (stat err = %v)", err)
		}
	})
}

// An auth error on the very first attempt must return immediately rather than
// entering the ticker loop, where it would retry a dead device code for the
// full expiry window.
func TestPollForToken_AuthErrorOnFirstAttemptReturnsImmediately(t *testing.T) {
	stubDeviceFlow(t,
		func(w http.ResponseWriter, r *http.Request) {},
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "expired_token"})
		},
	)

	done := make(chan error, 1)
	go func() {
		_, err := PollForToken(context.Background(), "dev", 5)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("err = %v, want an expiry error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PollForToken entered the poll loop instead of returning on an auth error")
	}
}

// The ticker path: a pending first attempt must be followed by a retry that
// succeeds. This is the loop a user actually sits through while approving in
// the browser.
func TestPollForToken_SucceedsOnASubsequentPoll(t *testing.T) {
	if testing.Short() {
		t.Skip("poll interval is clamped to 5s")
	}
	attempts := 0
	stubDeviceFlow(t,
		func(w http.ResponseWriter, r *http.Request) {},
		func(w http.ResponseWriter, r *http.Request) {
			attempts++
			if attempts == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "gho_late"})
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tok, err := PollForToken(ctx, "dev", 1) // clamped up to 5s
	if err != nil {
		t.Fatalf("PollForToken: %v", err)
	}
	if tok.AccessToken != "gho_late" {
		t.Fatalf("access token = %q, want gho_late", tok.AccessToken)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want at least 2", attempts)
	}
}

// SaveToken must refuse to write over an auth file it cannot parse. Replacing
// it would delete whatever other credentials the file holds — the same class of
// bug as config.Load vs LoadStrict.
func TestSaveToken_RefusesToClobberAnUnparseableAuthFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".joshbot")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{ this is not json")
	authFile := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authFile, corrupt, 0600); err != nil {
		t.Fatal(err)
	}

	err := SaveToken(home, &TokenInfo{AccessToken: "gho_new"})
	if err == nil {
		t.Fatal("SaveToken overwrote an unparseable auth file instead of erroring")
	}

	after, readErr := os.ReadFile(authFile)
	if readErr != nil {
		t.Fatalf("auth file unreadable after failed save: %v", readErr)
	}
	if string(after) != string(corrupt) {
		t.Fatalf("auth file was modified despite the error: %q", after)
	}
}

// SaveToken must not disturb credentials belonging to other providers stored in
// the same file.
func TestSaveToken_PreservesOtherEntries(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".joshbot")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	authFile := filepath.Join(dir, "auth.json")
	seed := map[string]TokenInfo{"some-other-provider": {AccessToken: "keep-me"}}
	data, _ := json.Marshal(seed)
	if err := os.WriteFile(authFile, data, 0600); err != nil {
		t.Fatal(err)
	}

	if err := SaveToken(home, &TokenInfo{AccessToken: "gho_new"}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	var got AuthData
	raw, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("auth file no longer parses: %v", err)
	}
	if got["some-other-provider"].AccessToken != "keep-me" {
		t.Errorf("unrelated entry lost: %+v", got)
	}
	if got["github-copilot"].AccessToken != "gho_new" {
		t.Errorf("github-copilot entry = %+v, want gho_new", got["github-copilot"])
	}

	info, err := os.Stat(authFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("auth.json mode = %o, want 0600 — it holds a bearer credential", perm)
	}
}

// AuthFilePath and LoadToken must agree on where auth.json lives; they compute
// it independently.
func TestAuthFilePath_MatchesWhereSaveTokenWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SaveToken(home, &TokenInfo{AccessToken: "gho_x"}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	path, err := AuthFilePath()
	if err != nil {
		t.Fatalf("AuthFilePath: %v", err)
	}
	// os.UserHomeDir reads $HOME on unix, so both should resolve into the
	// temporary home. Compare the resolved paths rather than the strings, since
	// macOS temp dirs are symlinked through /private.
	wantPath := filepath.Join(home, ".joshbot", "auth.json")
	gotReal, err1 := filepath.EvalSymlinks(path)
	wantReal, err2 := filepath.EvalSymlinks(wantPath)
	if err1 != nil || err2 != nil {
		t.Fatalf("EvalSymlinks: %v / %v", err1, err2)
	}
	if gotReal != wantReal {
		t.Errorf("AuthFilePath() = %q, but SaveToken wrote %q", gotReal, wantReal)
	}
}
