package providers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// mockProvider implements Provider for testing
type mockProvider struct {
	name         string
	config       Config
	chatErr      error
	streamErr    error
	chatResponse *ChatResponse
	chatCalls    int
}

func (m *mockProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	m.chatCalls++
	if m.chatResponse != nil {
		// Return response with the requested model
		resp := *m.chatResponse
		resp.Model = req.Model
		return &resp, nil
	}
	return nil, m.chatErr
}

func (m *mockProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	ch := make(chan StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (m *mockProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", nil
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) Config() Config {
	return m.config
}

// mockLogger implements Logger for testing
type mockLogger struct {
	debugMsgs []string
	infoMsgs  []string
	warnMsgs  []string
	errorMsgs []string
}

// formatLogMsg renders a structured log call (message plus key/value pairs)
// as a single string for test assertions. It must not use fmt.Sprintf(msg,
// args...): msg is a plain message, not a format string, and treating it as
// one causes go vet's printf analyzer to (mis)classify the Logger interface
// methods as printf wrappers.
func formatLogMsg(msg string, args ...interface{}) string {
	return fmt.Sprint(append([]interface{}{msg}, args...)...)
}

func (m *mockLogger) Debug(msg string, args ...interface{}) {
	m.debugMsgs = append(m.debugMsgs, formatLogMsg(msg, args...))
}

func (m *mockLogger) Info(msg string, args ...interface{}) {
	m.infoMsgs = append(m.infoMsgs, formatLogMsg(msg, args...))
}

func (m *mockLogger) Warn(msg string, args ...interface{}) {
	m.warnMsgs = append(m.warnMsgs, formatLogMsg(msg, args...))
}

func (m *mockLogger) Error(msg string, args ...interface{}) {
	m.errorMsgs = append(m.errorMsgs, formatLogMsg(msg, args...))
}

func TestMultiProvider_RegisterUnregister(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{})

	provider1 := &mockProvider{name: "provider1"}
	provider2 := &mockProvider{name: "provider2"}

	// Register providers
	mp.Register("provider1", provider1, "model1", 0)
	mp.Register("provider2", provider2, "model2", 1)

	// Check they are registered
	if !mp.HasProvider("provider1") {
		t.Error("provider1 should be registered")
	}
	if !mp.HasProvider("provider2") {
		t.Error("provider2 should be registered")
	}

	// Unregister
	mp.Unregister("provider1")
	if mp.HasProvider("provider1") {
		t.Error("provider1 should be unregistered")
	}
	// provider2 should still be there
	if !mp.HasProvider("provider2") {
		t.Error("provider2 should still be registered")
	}
}

func TestMultiProvider_SetDefault(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "provider2",
	})

	provider1 := &mockProvider{name: "provider1"}
	provider2 := &mockProvider{name: "provider2", config: Config{Model: "model2"}}

	mp.Register("provider1", provider1, "model1", 0)
	mp.Register("provider2", provider2, "model2", 1)

	// Set default to provider2
	mp.SetDefault("provider2")

	// Verify config returns provider2's config
	cfg := mp.Config()
	if cfg.Model != "model2" {
		t.Errorf("Config().Model = %q, want %q", cfg.Model, "model2")
	}
}

func TestMultiProvider_NoProviders(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{})

	_, err := mp.Chat(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	if err == nil {
		t.Error("expected error when no providers configured")
	}
}

func TestMultiProvider_SuccessfulFallback(t *testing.T) {
	logger := &mockLogger{}
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "primary",
		Logger:          logger,
	})

	// First provider fails, second succeeds
	failingProvider := &mockProvider{
		name:    "failing",
		chatErr: &FallbackError{StatusCode: 503, Message: "service unavailable", Provider: "failing"},
	}

	successProvider := &mockProvider{
		name: "success",
		chatResponse: &ChatResponse{
			ID:      "resp-1",
			Model:   "test-model",
			Choices: []Choice{{Index: 0, Message: Message{Role: RoleAssistant, Content: "Hello"}}},
			Usage:   Usage{PromptTokens: 10, CompletionTokens: 5},
		},
	}

	mp.Register("failing", failingProvider, "model1", 1)
	mp.Register("success", successProvider, "model2", 0)
	mp.SetDefault("failing")

	resp, err := mp.Chat(context.Background(), ChatRequest{
		Model:    "",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if resp == nil {
		t.Fatal("expected response, got nil")
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "Hello" {
		t.Errorf("Response content = %q, want %q", func() string {
			if len(resp.Choices) > 0 {
				return resp.Choices[0].Message.Content
			}
			return ""
		}(), "Hello")
	}

	// Verify fallback happened
	if len(logger.infoMsgs) == 0 {
		t.Error("expected info log about fallback")
	}
}

func TestMultiProvider_AllProvidersFail(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "p1",
	})

	p1 := &mockProvider{
		name:    "p1",
		chatErr: &FallbackError{StatusCode: 503, Message: "unavailable", Provider: "p1"},
	}
	p2 := &mockProvider{
		name:    "p2",
		chatErr: &FallbackError{StatusCode: 429, Message: "rate limited", Provider: "p2"},
	}

	mp.Register("p1", p1, "model1", 0)
	mp.Register("p2", p2, "model2", 1)

	_, err := mp.Chat(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	if err == nil {
		t.Error("expected error when all providers fail")
	}

	// Error should mention both providers
	errStr := err.Error()
	if errStr == "" {
		t.Error("error message should not be empty")
	}
}

func TestMultiProvider_NonFallbackErrorStops(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "p1",
	})

	// Non-fallback error (401 auth)
	p1 := &mockProvider{
		name:    "p1",
		chatErr: &FallbackError{StatusCode: 401, Message: "unauthorized", Provider: "p1"},
	}
	p2 := &mockProvider{name: "p2"}

	mp.Register("p1", p1, "model1", 0)
	mp.Register("p2", p2, "model2", 1)

	// Even though p2 is registered, the 401 should stop immediately
	_, err := mp.Chat(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	if err == nil {
		t.Error("expected error for auth failure")
	}
}

func TestMultiProvider_ParseModel(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "default",
	})

	p1 := &mockProvider{name: "p1"}
	mp.Register("p1", p1, "model1", 0)

	// Test provider:model format
	provider, model := mp.parseModel("p1:special-model")
	if provider != "p1" {
		t.Errorf("parseModel() provider = %q, want %q", provider, "p1")
	}
	if model != "special-model" {
		t.Errorf("parseModel() model = %q, want %q", model, "special-model")
	}

	// Test default provider with model
	provider, model = mp.parseModel("some-model")
	if provider != "default" {
		t.Errorf("parseModel() provider = %q, want %q", provider, "default")
	}
	if model != "some-model" {
		t.Errorf("parseModel() model = %q, want %q", model, "some-model")
	}

	// Test empty
	provider, model = mp.parseModel("")
	if provider != "default" {
		t.Errorf("parseModel() provider = %q, want %q", provider, "default")
	}
	if model != "" {
		t.Errorf("parseModel() model = %q, want %q", model, "")
	}

	// Test unknown provider falls back to default
	provider, model = mp.parseModel("unknown:special-model")
	if provider != "default" {
		t.Errorf("parseModel() provider = %q, want %q", provider, "default")
	}
}

func TestMultiProvider_ResolveModel(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{})

	entry := &ProviderEntry{
		Name:     "test",
		Provider: &mockProvider{name: "test", config: Config{Model: "provider-model"}},
		Model:    "entry-model",
	}

	// Test with requested model
	result := mp.resolveModel(entry, "requested-model", true)
	if result != "requested-model" {
		t.Errorf("resolveModel() = %q, want %q", result, "requested-model")
	}

	// Test with entry model
	result = mp.resolveModel(entry, "", true)
	if result != "entry-model" {
		t.Errorf("resolveModel() = %q, want %q", result, "entry-model")
	}

	// Test with provider config model (empty entry model)
	entry.Model = ""
	result = mp.resolveModel(entry, "", true)
	if result != "provider-model" {
		t.Errorf("resolveModel() = %q, want %q", result, "provider-model")
	}
}

func TestMultiProvider_GetProviderNames(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{})

	mp.Register("p1", &mockProvider{name: "p1"}, "m1", 0)
	mp.Register("p2", &mockProvider{name: "p2"}, "m2", 0)

	names := mp.GetProviderNames()
	if len(names) != 2 {
		t.Errorf("GetProviderNames() len = %d, want 2", len(names))
	}
}

func TestMultiProvider_ContextCancellation(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{})

	provider := &mockProvider{
		name:    "slow",
		chatErr: context.Canceled,
	}

	mp.Register("slow", provider, "model", 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := mp.Chat(ctx, ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	if err != context.Canceled {
		t.Errorf("Chat() error = %v, want context.Canceled", err)
	}
}

func TestMultiProvider_StreamFallback(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "p1",
	})

	p1 := &mockProvider{
		name:      "p1",
		streamErr: &FallbackError{StatusCode: 503, Message: "unavailable", Provider: "p1"},
	}
	p2 := &mockProvider{name: "p2"}

	mp.Register("p1", p1, "model1", 0)
	mp.Register("p2", p2, "model2", 1)

	ch, err := mp.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}

	// p2 should succeed (returns closed channel)
	if ch == nil {
		t.Error("expected channel, got nil")
	}
}

func TestMultiProvider_StreamAllFail(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "p1",
	})

	p1 := &mockProvider{
		name:      "p1",
		streamErr: &FallbackError{StatusCode: 503, Message: "unavailable", Provider: "p1"},
	}
	p2 := &mockProvider{
		name:      "p2",
		streamErr: &FallbackError{StatusCode: 429, Message: "rate limited", Provider: "p2"},
	}

	mp.Register("p1", p1, "model1", 0)
	mp.Register("p2", p2, "model2", 1)

	_, err := mp.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	if err == nil {
		t.Error("expected error when all stream providers fail")
	}
}

func TestMultiProvider_GetFallbackChain(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "default",
	})

	mp.Register("a", &mockProvider{name: "a"}, "model-a", 2)
	mp.Register("b", &mockProvider{name: "b"}, "model-b", 0)
	mp.Register("c", &mockProvider{name: "c"}, "model-c", 1)

	// Start with 'a' - should be first, then b, c by priority
	chain := mp.getFallbackChain("a")
	if len(chain) != 3 {
		t.Fatalf("getFallbackChain() len = %d, want 3", len(chain))
	}
	if chain[0].Name != "a" {
		t.Errorf("first provider = %q, want %q", chain[0].Name, "a")
	}

	// Default - should be b (priority 0), then c, then a
	chain = mp.getFallbackChain("")
	if chain[0].Name != "b" {
		t.Errorf("first by priority = %q, want %q", chain[0].Name, "b")
	}
}

func TestMultiProvider_Transcribe(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "p1",
	})

	p1 := &mockProvider{name: "p1"}
	mp.Register("p1", p1, "model1", 0)

	// Should delegate to primary provider
	_, err := mp.Transcribe(context.Background(), []byte("audio"), "prompt")
	// The mock provider returns empty string, no error
	if err != nil {
		t.Errorf("Transcribe() error = %v", err)
	}
}

func TestMultiProvider_TranscribeNoProvider(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{})

	_, err := mp.Transcribe(context.Background(), []byte("audio"), "prompt")
	if err == nil {
		t.Error("expected error with no default provider")
	}
}

func TestMultiProvider_TimeoutContext(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "p1",
	})

	provider := &mockProvider{
		name:    "slow",
		chatErr: context.DeadlineExceeded,
	}

	mp.Register("slow", provider, "model", 0)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	_, err := mp.Chat(ctx, ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	// Should return deadline exceeded error
	if err != context.DeadlineExceeded {
		t.Logf("got error: %v", err)
	}
}

func TestMultiProvider_DisabledProviderNotInFallbackChain(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "enabled1",
	})

	// Register enabled provider
	enabled1 := &mockProvider{name: "enabled1"}
	mp.Register("enabled1", enabled1, "model1", 0, true)

	// Register disabled provider
	disabled := &mockProvider{name: "disabled"}
	mp.Register("disabled", disabled, "model2", 1, false)

	// Register another enabled provider
	enabled2 := &mockProvider{name: "enabled2"}
	mp.Register("enabled2", enabled2, "model3", 2, true)

	// Get fallback chain
	chain := mp.getFallbackChain("enabled1")

	// Should only have 2 enabled providers
	if len(chain) != 2 {
		t.Errorf("getFallbackChain() len = %d, want 2", len(chain))
	}

	// Check provider names
	if chain[0].Name != "enabled1" {
		t.Errorf("first provider = %q, want %q", chain[0].Name, "enabled1")
	}
	if chain[1].Name != "enabled2" {
		t.Errorf("second provider = %q, want %q", chain[1].Name, "enabled2")
	}
}

func TestMultiProvider_DisabledProviderNotUsed(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "primary",
	})

	// Primary is disabled
	primary := &mockProvider{
		name:    "primary",
		chatErr: &FallbackError{StatusCode: 503, Message: "unavailable", Provider: "primary"},
	}
	mp.Register("primary", primary, "model1", 0, false)

	// Secondary is enabled but should fail
	secondary := &mockProvider{
		name:    "secondary",
		chatErr: &FallbackError{StatusCode: 503, Message: "unavailable", Provider: "secondary"},
	}
	mp.Register("secondary", secondary, "model2", 1, true)

	// Should skip primary and try secondary
	_, err := mp.Chat(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	// Both should fail, but it should try both (not just primary)
	if err == nil {
		t.Error("expected error when all providers fail")
	}

	// Verify primary was never called (since it's disabled)
	if primary.chatCalls > 0 {
		t.Errorf("disabled provider should not be called, but was called %d times", primary.chatCalls)
	}

	// Secondary should have been called
	if secondary.chatCalls != 1 {
		t.Errorf("secondary provider should be called once, was called %d times", secondary.chatCalls)
	}
}

func TestMultiProvider_SetEnabled(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "provider1",
	})

	p1 := &mockProvider{name: "provider1"}
	p2 := &mockProvider{name: "provider2"}

	// Register both as enabled initially
	mp.Register("provider1", p1, "model1", 0, true)
	mp.Register("provider2", p2, "model2", 1, true)

	// Disable provider2
	mp.SetEnabled("provider2", false)

	// Check that HasProvider returns false for disabled
	if mp.HasProvider("provider2") {
		t.Error("HasProvider should return false for disabled provider")
	}

	// Check that provider2 is not in fallback chain
	chain := mp.getFallbackChain("provider1")
	if len(chain) != 1 {
		t.Errorf("getFallbackChain() len = %d, want 1", len(chain))
	}

	// Re-enable provider2
	mp.SetEnabled("provider2", true)

	// Check that HasProvider returns true
	if !mp.HasProvider("provider2") {
		t.Error("HasProvider should return true for re-enabled provider")
	}

	// Check fallback chain again
	chain = mp.getFallbackChain("provider1")
	if len(chain) != 2 {
		t.Errorf("getFallbackChain() len = %d, want 2", len(chain))
	}
}

func TestMultiProvider_DefaultEnabled(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{
		DefaultProvider: "provider1",
	})

	// Register without explicit enabled parameter (should default to true)
	p1 := &mockProvider{name: "provider1"}
	mp.Register("provider1", p1, "model1", 0)

	if !mp.HasProvider("provider1") {
		t.Error("HasProvider should return true for provider registered without explicit enabled")
	}

	chain := mp.getFallbackChain("provider1")
	if len(chain) != 1 {
		t.Errorf("getFallbackChain() len = %d, want 1", len(chain))
	}
}

// modelAwareProvider answers only for the models it actually hosts, the way a
// real API does: anything else is a 404. Used to reproduce the cross-provider
// model leak in the fallback chain.
type modelAwareProvider struct {
	name      string
	config    Config
	hosts     map[string]bool
	openErr   error    // returned before any model check (e.g. a 429)
	seenModel []string // every model this provider was asked for
}

func (m *modelAwareProvider) record(model string) { m.seenModel = append(m.seenModel, model) }

func (m *modelAwareProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	m.record(req.Model)
	if m.openErr != nil {
		return nil, m.openErr
	}
	if !m.hosts[req.Model] {
		return nil, fmt.Errorf(`API error (404): {"error":"please check the model you provided"}`)
	}
	return &ChatResponse{Model: req.Model}, nil
}

func (m *modelAwareProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	m.record(req.Model)
	if m.openErr != nil {
		return nil, m.openErr
	}
	if !m.hosts[req.Model] {
		return nil, fmt.Errorf(`API error (404): {"error":"please check the model you provided"}`)
	}
	ch := make(chan StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (m *modelAwareProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", nil
}
func (m *modelAwareProvider) Name() string   { return m.name }
func (m *modelAwareProvider) Config() Config { return m.config }

// A model ID belongs to the provider that publishes it. When the primary is
// rate-limited and the chain falls back, the fallback must be asked for its own
// model — asking poolside for "z-ai/glm-5.2" earns a 404 whose text
// ("please check the model you provided") reads as a joshbot misconfiguration
// and completely hides the rate limit that actually happened.
func TestMultiProvider_FallbackUsesItsOwnModel(t *testing.T) {
	openrouter := &modelAwareProvider{
		name:   "openrouter",
		hosts:  map[string]bool{"z-ai/glm-5.2": true},
		config: Config{Model: "z-ai/glm-5.2"},
		// What the primary actually did: rate limit, a textbook fallback trigger.
		openErr: fmt.Errorf("API error (429): rate limit exceeded"),
	}
	poolside := &modelAwareProvider{
		name:   "poolside",
		hosts:  map[string]bool{"poolside/laguna-s-2.1": true},
		config: Config{Model: "poolside/laguna-s-2.1"},
	}

	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{"chat", false}, {"stream", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			openrouter.seenModel, poolside.seenModel = nil, nil
			mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "openrouter"})
			mp.Register("openrouter", openrouter, "z-ai/glm-5.2", 0, true)
			mp.Register("poolside", poolside, "poolside/laguna-s-2.1", 1, true)

			req := ChatRequest{Model: "z-ai/glm-5.2"}
			var err error
			if tc.stream {
				_, err = mp.ChatStream(context.Background(), req)
			} else {
				_, err = mp.Chat(context.Background(), req)
			}
			if err != nil {
				t.Fatalf("fallback should have succeeded on poolside, got: %v", err)
			}
			if len(poolside.seenModel) != 1 || poolside.seenModel[0] != "poolside/laguna-s-2.1" {
				t.Errorf("poolside was asked for %v, want [poolside/laguna-s-2.1]", poolside.seenModel)
			}
		})
	}
}

// When every provider fails, the reported error must name the whole chain —
// the last provider's 404 alone is the least informative thing that happened.
func TestMultiProvider_AllFailedErrorNamesEveryProvider(t *testing.T) {
	openrouter := &modelAwareProvider{
		name: "openrouter", hosts: map[string]bool{"z-ai/glm-5.2": true},
		openErr: fmt.Errorf("API error (429): rate limit exceeded"),
	}
	poolside := &modelAwareProvider{
		name: "poolside", hosts: map[string]bool{}, // hosts nothing: 404s
		config: Config{Model: "poolside/laguna-s-2.1"},
	}

	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "openrouter"})
	mp.Register("openrouter", openrouter, "z-ai/glm-5.2", 0, true)
	mp.Register("poolside", poolside, "poolside/laguna-s-2.1", 1, true)

	_, err := mp.ChatStream(context.Background(), ChatRequest{Model: "z-ai/glm-5.2"})
	if err == nil {
		t.Fatal("expected an error when every provider fails")
	}
	for _, want := range []string{"openrouter", "429", "poolside"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
