package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These cover provider paths that fail silently when they regress: a config
// field dropped on the wire, a key pool that rotates the wrong way, and the
// model-listing refusal that keeps credential validation honest.

// ExtraBody exists for providers that need a custom JSON field (poolside's
// chat_template_kwargs). It was merged only in ChatStream, so every
// non-streaming turn sent a body without it and the provider answered as if
// the setting had never been configured — no error anywhere.
func TestExtraBodyReachesTheWireOnBothChatAndChatStream(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "Chat"
		if streaming {
			name = "ChatStream"
		}
		t.Run(name, func(t *testing.T) {
			bodies := make(chan map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var got map[string]any
				_ = json.Unmarshal(raw, &got)
				bodies <- got
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
					return
				}
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
			}))
			defer srv.Close()

			p := NewLiteLLMProvider(Config{
				APIBase:   srv.URL,
				APIKey:    "sk-test",
				Model:     "m",
				ExtraBody: map[string]any{"chat_template_kwargs": map[string]any{"thinking": true}},
			})
			req := ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}

			var err error
			if streaming {
				var ch <-chan StreamChunk
				ch, err = p.ChatStream(context.Background(), req)
				if err == nil {
					for range ch { //nolint:revive // drain
					}
				}
			} else {
				_, err = p.Chat(context.Background(), req)
			}
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}

			got := <-bodies
			if _, ok := got["chat_template_kwargs"]; !ok {
				keys := make([]string, 0, len(got))
				for k := range got {
					keys = append(keys, k)
				}
				t.Fatalf("ExtraBody never reached the provider; body had keys %v", keys)
			}
		})
	}
}

// setKeyRecorder records what the pool pushed down to the wrapped provider, so
// a rotation that updates bookkeeping without changing the credential in use
// is visible.
type setKeyRecorder struct {
	Provider
	keys []string
}

func (r *setKeyRecorder) SetAPIKey(k string) { r.keys = append(r.keys, k) }

type stubProvider struct{}

func (stubProvider) Chat(context.Context, ChatRequest) (*ChatResponse, error) {
	return nil, errors.New("unused")
}
func (stubProvider) ChatStream(context.Context, ChatRequest) (<-chan StreamChunk, error) {
	return nil, errors.New("unused")
}
func (stubProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", errors.New("unused")
}
func (stubProvider) Name() string     { return "stub" }
func (stubProvider) Config() Config   { return DefaultConfig() }
func (stubProvider) SetAPIKey(string) {}

// Rotation is a security-adjacent decision: rotating on the wrong status
// burns every key on a fault that was never key-specific, and failing to
// rotate on 401/402/429 leaves the run pinned to a dead credential. Both
// failures look like "the provider is down".
func TestKeyRotationOnlyFiresOnKeySpecificFailuresAndSwapsTheLiveKey(t *testing.T) {
	inner := &setKeyRecorder{Provider: stubProvider{}}
	// cooldownAfterFailures 1 so a single reported failure retires a key: the
	// point of the last assertion is the exhausted path, not the retry budget.
	p := NewKeyRotatingProvider(inner, NewAPIKeyPool([]string{"k1", "k2"}, time.Hour, 1))

	// A 500 is the provider's fault, not the key's: rotating here would spend
	// the pool on an outage.
	serverErr := errors.New("HTTP 500: upstream is down")
	if err := p.rotateOnFailure(serverErr); !errors.Is(err, serverErr) {
		t.Fatalf("a 500 triggered rotation, err=%v", err)
	}
	if len(inner.keys) != 0 {
		t.Fatalf("key changed on a non-key failure: %v", inner.keys)
	}

	for _, code := range []int{429, 402, 401} {
		if !shouldRotateKey(fmt.Errorf("HTTP %d: nope", code)) {
			t.Errorf("HTTP %d did not warrant rotation", code)
		}
	}

	// A rotatable failure must swap the credential the inner provider uses,
	// not merely mark the old one bad.
	before := len(inner.keys)
	if err := p.rotateOnFailure(errors.New("HTTP 429: rate limited")); err != nil {
		t.Fatalf("rotation with a spare key failed: %v", err)
	}
	if len(inner.keys) != before+1 {
		t.Fatalf("rotation did not push a new key down: %v", inner.keys)
	}

	// Once every key has failed, the caller must be told — silently reusing an
	// exhausted pool retries forever against credentials known to be dead.
	var last error
	for i := 0; i < 5; i++ {
		last = p.rotateOnFailure(errors.New("HTTP 429: rate limited"))
	}
	if last == nil || !strings.Contains(last.Error(), "exhausted") {
		t.Fatalf("an exhausted pool reported %v, want an exhausted error", last)
	}
}

// ListModels used to default an empty APIBase to openrouter.ai, so credential
// validation for any other provider dialled OpenRouter and printed a tick.
// This is the boundary that lets configure report "could not verify".
func TestListModelsRefusesAnEmptyAPIBaseRatherThanDiallingOpenRouter(t *testing.T) {
	_, err := ListModels(Config{APIKey: "sk-x"})
	if err == nil {
		t.Fatal("an empty APIBase was accepted; validation would hit openrouter.ai")
	}
	if strings.Contains(err.Error(), "sk-x") {
		t.Errorf("the API key leaked into the error: %v", err)
	}
}

// An HTTP error body is attacker-influenced and routinely contains the key the
// caller just sent. It must not come back out in the error a user sees.
func TestProviderErrorsDoNotEchoTheAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"invalid key %s"}}`, r.Header.Get("Authorization"))
	}))
	defer srv.Close()

	p := NewLiteLLMProvider(Config{APIBase: srv.URL, APIKey: "sk-liveKEY0123456789abcdefXYZ", Model: "m"})
	_, err := p.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}
	if strings.Contains(err.Error(), "sk-liveKEY0123456789abcdefXYZ") {
		t.Fatalf("the API key reached the error text: %v", err)
	}
}
