package skills

import (
	"context"
	"errors"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// mockExtractProvider implements providers.Provider for testing extraction.
type mockExtractProvider struct {
	chatFn func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error)
}

func (m *mockExtractProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if m.chatFn != nil {
		return m.chatFn(ctx, req)
	}
	return &providers.ChatResponse{
		Choices: []providers.Choice{
			{Message: providers.Message{Content: "mock skill content"}},
		},
	}, nil
}

func (m *mockExtractProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, errors.New("not implemented")
}

func (m *mockExtractProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", errors.New("not implemented")
}

func (m *mockExtractProvider) Name() string { return "mock-extract" }
func (m *mockExtractProvider) Config() providers.Config {
	return providers.DefaultConfig()
}

func TestNewExtractor(t *testing.T) {
	prov := &mockExtractProvider{}
	e := NewExtractor(prov, "")

	if e == nil {
		t.Fatal("NewExtractor() returned nil")
	}
}

func TestNewExtractor_WithModel(t *testing.T) {
	prov := &mockExtractProvider{}
	e := NewExtractor(prov, "gpt-4")

	if e == nil {
		t.Fatal("NewExtractor() returned nil")
	}
}

func TestExtractor_Extract_Success(t *testing.T) {
	var capturedReq providers.ChatRequest
	prov := &mockExtractProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			capturedReq = req
			return &providers.ChatResponse{
				Choices: []providers.Choice{
					{Message: providers.Message{Content: "---\nname: test-skill\ndescription: A test skill\n---\n\nStep 1: Do something."}},
				},
			}, nil
		},
	}

	e := NewExtractor(prov, "")
	ctx := context.Background()

	trace := Trace{
		UserMessage: "find and replace text in file",
		ToolCalls: []ToolCallRecord{
			{Tool: "grep", Args: map[string]any{"pattern": "old"}, Result: "found it"},
			{Tool: "write_file", Args: map[string]any{"path": "file.txt"}, Result: "written"},
		},
		FinalOutput: "Replaced the text.",
	}

	content, err := e.Extract(ctx, trace, nil)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}

	if content == "" {
		t.Fatal("Extract() returned empty content")
	}

	// Verify the request was built correctly
	if len(capturedReq.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(capturedReq.Messages))
	}
	if capturedReq.MaxTokens != 2000 {
		t.Errorf("expected MaxTokens 2000, got %d", capturedReq.MaxTokens)
	}
	if capturedReq.Temperature != 0.3 {
		t.Errorf("expected Temperature 0.3, got %f", capturedReq.Temperature)
	}

	// Verify the prompt contains trace information
	msg := capturedReq.Messages[0].Content
	if !contains(msg, "find and replace text in file") {
		t.Error("expected prompt to contain UserMessage")
	}
	if !contains(msg, "grep") {
		t.Error("expected prompt to contain tool names")
	}
	if !contains(msg, "write_file") {
		t.Error("expected prompt to contain write_file tool name")
	}
	if !contains(msg, "Replaced the text.") {
		t.Error("expected prompt to contain FinalOutput")
	}
}

func TestExtractor_Extract_WithModel(t *testing.T) {
	prov := &mockExtractProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			if req.Model != "claude-3" {
				t.Errorf("expected model 'claude-3', got %q", req.Model)
			}
			return &providers.ChatResponse{
				Choices: []providers.Choice{
					{Message: providers.Message{Content: "---\nname: my-skill\n---\nbody"}},
				},
			}, nil
		},
	}

	e := NewExtractor(prov, "claude-3")
	ctx := context.Background()

	trace := Trace{
		UserMessage: "do something",
		ToolCalls:   []ToolCallRecord{},
		FinalOutput: "done",
	}

	content, err := e.Extract(ctx, trace, nil)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if content == "" {
		t.Fatal("Extract() returned empty content")
	}
}

func TestExtractor_Extract_WithExistingSkills(t *testing.T) {
	var capturedReq providers.ChatRequest
	prov := &mockExtractProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			capturedReq = req
			return &providers.ChatResponse{
				Choices: []providers.Choice{
					{Message: providers.Message{Content: "---\nname: new-skill\n---\nbody"}},
				},
			}, nil
		},
	}

	e := NewExtractor(prov, "")
	ctx := context.Background()

	trace := Trace{
		UserMessage: "do something",
		ToolCalls:   []ToolCallRecord{},
		FinalOutput: "done",
	}

	existingSkills := []*Skill{
		{Name: "existing-skill-1"},
		{Name: "existing-skill-2"},
	}

	content, err := e.Extract(ctx, trace, existingSkills)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if content == "" {
		t.Fatal("Extract() returned empty content")
	}

	// Verify the prompt includes existing skills
	if !contains(capturedReq.Messages[0].Content, "existing-skill-1") {
		t.Error("expected prompt to mention existing-skill-1")
	}
	if !contains(capturedReq.Messages[0].Content, "existing-skill-2") {
		t.Error("expected prompt to mention existing-skill-2")
	}
}

func TestExtractor_Extract_ProviderError(t *testing.T) {
	prov := &mockExtractProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return nil, errors.New("api call failed")
		},
	}

	e := NewExtractor(prov, "")
	ctx := context.Background()

	trace := Trace{
		UserMessage: "do something",
		ToolCalls:   []ToolCallRecord{},
		FinalOutput: "done",
	}

	_, err := e.Extract(ctx, trace, nil)
	if err == nil {
		t.Fatal("expected error for provider failure")
	}
	if !contains(err.Error(), "api call failed") {
		t.Errorf("expected error to contain 'api call failed', got: %v", err)
	}
}

func TestExtractor_Extract_NoChoices(t *testing.T) {
	prov := &mockExtractProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{
				Choices: []providers.Choice{},
			}, nil
		},
	}

	e := NewExtractor(prov, "")
	ctx := context.Background()

	trace := Trace{
		UserMessage: "do something",
		ToolCalls:   []ToolCallRecord{},
		FinalOutput: "done",
	}

	_, err := e.Extract(ctx, trace, nil)
	if err == nil {
		t.Fatal("expected error for no choices")
	}
	if !contains(err.Error(), "no choices") {
		t.Errorf("expected error to contain 'no choices', got: %v", err)
	}
}

func TestExtractor_Extract_TrimsContent(t *testing.T) {
	prov := &mockExtractProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{
				Choices: []providers.Choice{
					{Message: providers.Message{Content: "  \n---\nname: trimmed\n---\nbody\n  "}},
				},
			}, nil
		},
	}

	e := NewExtractor(prov, "")
	ctx := context.Background()

	trace := Trace{
		UserMessage: "do something",
		ToolCalls:   []ToolCallRecord{},
		FinalOutput: "done",
	}

	content, err := e.Extract(ctx, trace, nil)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if content != "---\nname: trimmed\n---\nbody" {
		t.Errorf("expected trimmed content, got %q", content)
	}
}

func TestExtractor_Extract_NoExistingSkills(t *testing.T) {
	var capturedReq providers.ChatRequest
	prov := &mockExtractProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			capturedReq = req
			return &providers.ChatResponse{
				Choices: []providers.Choice{
					{Message: providers.Message{Content: "---\nname: solo\n---\nbody"}},
				},
			}, nil
		},
	}

	e := NewExtractor(prov, "")
	ctx := context.Background()

	trace := Trace{
		UserMessage: "do something",
		ToolCalls:   []ToolCallRecord{},
		FinalOutput: "done",
	}

	// Pass empty slice vs nil - both should work
	_, err := e.Extract(ctx, trace, []*Skill{})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}

	// Should NOT contain "Existing skills:" since list is empty
	if contains(capturedReq.Messages[0].Content, "Existing skills:") {
		t.Error("expected no 'Existing skills:' section for empty existing skills")
	}
}
