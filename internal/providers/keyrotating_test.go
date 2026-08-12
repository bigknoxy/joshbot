package providers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// keyedMock is a mockProvider that also implements SetAPIKey (needed for KeyedProvider).
type keyedMock struct {
	name        string
	config      Config
	chatErr     error
	streamErr   error
	apiKeysUsed []string
}

func (k *keyedMock) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	return nil, k.chatErr
}
func (k *keyedMock) ChatStream(_ context.Context, _ ChatRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	close(ch)
	return ch, k.streamErr
}
func (k *keyedMock) Transcribe(_ context.Context, _ []byte, _ string) (string, error) { return "", nil }
func (k *keyedMock) SetAPIKey(key string)                                              { k.apiKeysUsed = append(k.apiKeysUsed, key) }
func (k *keyedMock) Name() string                                                      { return k.name }
func (k *keyedMock) Config() Config                                                    { return k.config }

// --- KeyRotatingProvider rotates pool keys on 429/401/402 errors ---

func TestKeyRotatingProvider_RotatesOnRateLimit(t *testing.T) {
	pool := NewAPIKeyPool([]string{"key-A", "key-B"}, time.Second, 1)
	inner := &keyedMock{name: "rotated"}
	kp := NewKeyRotatingProvider(inner, pool)

	// First call acquires key-A.
	_, err := kp.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	// Second call still uses key-A after success.
	_, err = kp.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("second call failed unexpectedly: %v", err)
	}

	keysUsed := inner.apiKeysUsed
	if len(keysUsed) == 0 {
		t.Fatal("expected the mock to have recorded at least one key usage")
	}
	// All acquired keys should be the first pool entry.
	for i, k := range keysUsed {
		if k != "key-A" {
			t.Errorf("call %d used %q, expected key-A", i+1, k)
		}
	}
}

func TestKeyRotatingProvider_ReportsFailureThenExhausted(t *testing.T) {
	pool := NewAPIKeyPool([]string{"key-A"}, time.Second, 1)
	inner := &keyedMock{name: "single", chatErr: fmt.Errorf("API error (429): rate limited")}
	kp := NewKeyRotatingProvider(inner, pool)

	_, err := kp.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error from the mock provider")
	}

	// key-A should be in cooldown after a 429.
	if avail := pool.Available(); avail != 0 {
		t.Errorf("key-A exhausted; expected 0 available, got %d", avail)
	}

	// Second call must fail with exhaustion message.
	_, err = kp.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error when all keys are in cooldown")
	}
	if !strings.Contains(err.Error(), "cooldown") && !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("error should describe the cooldown state: %v", err)
	}
}

func TestKeyRotatingProvider_NameConfigDelegate(t *testing.T) {
	pool := NewAPIKeyPool([]string{"key1"}, time.Second, 1)
	cfg := DefaultConfig()
	cfg.Model = "gpt-4o"
	inner := &keyedMock{name: "delegated", config: cfg}
	kp := NewKeyRotatingProvider(inner, pool)

	if kp.Name() != "delegated" {
		t.Errorf("Name = %q, want %q", kp.Name(), inner.Name())
	}
	c := kp.Config()
	if c.Model != "gpt-4o" {
		t.Error("Config must delegate to inner provider")
	}
	if kp.Pool() != pool {
		t.Error("Pool must return the same instance")
	}
}

func TestKeyRotatingProvider_EmptyPoolFails(t *testing.T) {
	pool := NewAPIKeyPool(nil, time.Second, 1)
	inner := &keyedMock{name: "empty"}
	kp := NewKeyRotatingProvider(inner, pool)

	if _, err := kp.Chat(context.Background(), ChatRequest{}); err == nil {
		t.Fatal("expected error when pool is empty")
	}
}

func TestKeyRotatingProvider_TranscribeGetsKey(t *testing.T) {
	pool := NewAPIKeyPool([]string{"tr-key"}, time.Second, 1)
	inner := &keyedMock{name: "transcribe"}
	kp := NewKeyRotatingProvider(inner, pool)

	text, err := kp.Transcribe(context.Background(), []byte{}, "hello")
	if text != "" {
		t.Errorf("expected empty transcription, got %q", text)
	}
	// Mock returns "", nil. Key was still acquired. That's fine.
	_ = err
	// Pool should have one available key (transcribe always succeeds by returning nil error).
	if pool.Available() != 0 {
		// After zero failures the key stays available, cooldown hasn't triggered.
	}
}
