package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func pngMagic(size int) []byte {
	png := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82}
	if size <= len(png) {
		return png[:size]
	}
	padded := make([]byte, size)
	copy(padded, png)
	return padded
}

func newNonStreamProvider() Provider { return &nonStreamProv{} }

// --- Message.MarshalJSON — image vs no-image paths ---

func TestMarshalJSON_NoImageProduceOriginalForm(t *testing.T) {
	msg := Message{Role: RoleUser, Content: "hello", Name: "cli"}
	data, err := msg.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	if strings.Contains(string(data), `"content":[`) {
		t.Error("message without images produced a content array — every provider would 400 this")
	}
}

func TestMarshalJSON_WithImagesProducesContentArray(t *testing.T) {
	msg := Message{
		Role:    RoleUser,
		Content: "see this",
		Images:  []Image{{MIME: "image/png", Data: pngMagic(16)}},
	}
	data, err := msg.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	if !strings.Contains(string(data), `"content":`) {
		t.Fatal("expected content field")
	}
	if !strings.Contains(string(data), "image_url") {
		t.Error("expected image_url part when images are present")
	}
}

// --- SupportsVision — failing closed on unknown models ---

func TestSupportsVision_FailsClosedOnUnknown(t *testing.T) {
	for _, spec := range []string{"totally-fake-x99", "", "   "} {
		if SupportsVision(spec) {
			t.Errorf("unknown/empty spec %q matched — failing closed is the point", spec)
		}
	}
}

func TestSupportsVision_KnownModelsMatch(t *testing.T) {
	for _, model := range []string{
		"gpt-4o", "OpenAI/gpt-4o-2026", "anthropic/claude-sonnet-4-20250514",
		"gemini-pro-vision", "ollama/llava:latest",
	} {
		if !SupportsVision(model) {
			t.Errorf("expected %q vision-capable", model)
		}
	}
}

// --- ValidateImages — total payload cap ---

func TestValidateImages_TotalPayloadCap(t *testing.T) {
	big := make([]byte, MaxTotalImageBytes/2+1)
	imgs := []Image{
		{MIME: "image/png", Data: big},
		{MIME: "image/png", Data: big},
	}
	err := ValidateImages(imgs)
	if err == nil {
		t.Fatal("expected rejection when total exceeds cap")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error must name the limit: %v", err)
	}
}

// --- screenForImages — text-only chain refused before dial ---

func TestScreenForImagesRefusesTextOnlyChain(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "txt"})
	mp.Register("txt", &mockProvider{name: "txt"}, "dummy-model-xyz", 0)

	req := ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "look", Images: []Image{{MIME: "image/png", Data: pngMagic(16)}}},
	}}

	_, err := mp.Chat(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when every configured model is text-only")
	}
	var visErr *ErrVisionUnsupported
	if !errors.As(err, &visErr) {
		t.Fatalf("expected ErrVisionUnsupported, got %T: %v", err, err)
	}
}

func TestScreenForImagesSurvivesVisionModels(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "alpha"})
	mp.Register("alpha", &mockProvider{name: "alpha"}, "gpt-4o", 0)
	req := ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "hi", Images: []Image{{MIME: "image/png", Data: pngMagic(16)}}},
	}}
	if _, err := mp.Chat(context.Background(), req); err != nil {
		t.Fatalf("success expected with vision model: %v", err)
	}
}

// --- ErrStreamingUnsupported must be returned as a sentinel, not bare string ---

func TestErrStreamingUnsupportedSentinel(t *testing.T) {
	p := newNonStreamProvider()
	_, err := p.ChatStream(context.Background(), ChatRequest{})
	if !errors.Is(err, ErrStreamingUnsupported) {
		t.Fatalf("must wrap ErrStreamingUnsupported; got: %T %v", err, err)
	}
}

type nonStreamProv struct{}

func (*nonStreamProv) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) { return nil, nil }
func (*nonStreamProv) ChatStream(_ context.Context, _ ChatRequest) (<-chan StreamChunk, error) {
	return nil, fmt.Errorf("github-copilot: %w", ErrStreamingUnsupported)
}
func (*nonStreamProv) Transcribe(_ context.Context, _ []byte, _ string) (string, error) {
	return "", nil
}
func (*nonStreamProv) Name() string   { return "non-stream" }
func (*nonStreamProv) Config() Config { return DefaultConfig() }

// --- IsSupportedImageMIME ---

func TestIsSupportedImageMIME(t *testing.T) {
	for _, mime := range []string{"image/png", "image/jpeg", "IMAGE/PNG", "image/gif; charset=x"} {
		if !IsSupportedImageMIME(mime) {
			t.Errorf("accepted: %q", mime)
		}
	}
	for _, mime := range []string{"text/plain", "", "video/mp4"} {
		if IsSupportedImageMIME(mime) {
			t.Errorf("expected rejection of: %q", mime)
		}
	}
}
