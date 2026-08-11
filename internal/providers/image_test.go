package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strings"
	"testing"
)

// pngBytes builds a real PNG so detection is exercised against actual magic
// bytes rather than a hand-written header that happens to satisfy the sniffer.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// TestTextOnlyMessageSerialisationIsUnchanged is the compatibility contract for
// the whole feature. Every provider receives this JSON, so a message with no
// image that gained a field or a content array would be a 400 on every request
// joshbot makes — a failure that looks like a provider outage rather than a
// serialisation change.
func TestTextOnlyMessageSerialisationIsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "plain user message",
			msg:  Message{Role: RoleUser, Content: "hello"},
			want: `{"role":"user","content":"hello"}`,
		},
		{
			name: "tool result",
			msg:  Message{Role: RoleTool, Content: "ok", Name: "shell", ToolCallID: "call_1"},
			want: `{"role":"tool","content":"ok","name":"shell","tool_call_id":"call_1"}`,
		},
		{
			name: "assistant with a tool call",
			msg: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{
				ID: "call_1", Type: "function",
				Function: FunctionCall{Name: "shell", Arguments: `{"command":"ls"}`},
			}}},
			want: `{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function",` +
				`"function":{"name":"shell","arguments":"{\"command\":\"ls\"}"}}]}`,
		},
		{
			name: "an empty Images slice is still text-only",
			msg:  Message{Role: RoleUser, Content: "hello", Images: []Image{}},
			want: `{"role":"user","content":"hello"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("text-only serialisation changed:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestMessageWithImageSerialisesAsContentParts pins the multimodal wire shape:
// content stops being a string and becomes an ordered parts array, text first.
func TestMessageWithImageSerialisesAsContentParts(t *testing.T) {
	img, err := NewImage("shot.png", pngBytes(t))
	if err != nil {
		t.Fatalf("new image: %v", err)
	}
	data, err := json.Marshal(Message{Role: RoleUser, Content: "what is this?", Images: []Image{img}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var wire struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("content must be an array of parts, got %s (%v)", data, err)
	}
	if len(wire.Content) != 2 {
		t.Fatalf("want a text part and an image part, got %d: %s", len(wire.Content), data)
	}
	if wire.Content[0].Type != "text" || wire.Content[0].Text != "what is this?" {
		t.Fatalf("the text part must come first and carry the content: %s", data)
	}
	if wire.Content[1].Type != "image_url" || wire.Content[1].ImageURL == nil {
		t.Fatalf("the image part is missing or malformed: %s", data)
	}
	if !strings.HasPrefix(wire.Content[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("the image must be a data URL carrying the sniffed type, got %q",
			wire.Content[1].ImageURL.URL)
	}

	// An image with no caption must not emit an empty text part: an empty
	// string is not a valid text part and providers reject it.
	bare, err := json.Marshal(Message{Role: RoleUser, Images: []Image{img}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(bare, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire.Content) != 1 || wire.Content[0].Type != "image_url" {
		t.Fatalf("an image with no caption must emit only the image part: %s", bare)
	}
}

// TestImageTypeIsDetectedByContentNotName — a filename is attacker-supplied on
// Telegram and meaningless in a chat. Trusting it sends a text file to the
// provider as an image and gets back an opaque 400.
func TestImageTypeIsDetectedByContentNotName(t *testing.T) {
	img, err := NewImage("notes.txt", pngBytes(t))
	if err != nil {
		t.Fatalf("a PNG named .txt must be accepted as a PNG: %v", err)
	}
	if img.MIME != "image/png" {
		t.Fatalf("detected type = %q, want image/png", img.MIME)
	}

	_, err = NewImage("photo.png", []byte("this is plainly not an image, it is prose.\n"))
	if err == nil {
		t.Fatal("a text file named .png must be rejected")
	}
	if !strings.Contains(err.Error(), "text/plain") {
		t.Fatalf("the error must name the type actually detected, got %v", err)
	}
}

// TestImageSizeLimitsAreEnforced — the constants exist to keep one message from
// being unsendable. Declaring them without checking them is the usual way this
// goes wrong.
func TestImageSizeLimitsAreEnforced(t *testing.T) {
	t.Run("per image", func(t *testing.T) {
		big := make([]byte, MaxImageBytes+1)
		copy(big, pngBytes(t))
		_, err := NewImage("big.png", big)
		if err == nil {
			t.Fatal("an image over the per-image limit must be rejected")
		}
		if !strings.Contains(err.Error(), "MiB") {
			t.Fatalf("the error must name the limit and the actual size, got %v", err)
		}
	})

	t.Run("total payload", func(t *testing.T) {
		// Several images each under the per-image limit, together over the
		// total: the case the per-image check cannot see.
		var images []Image
		for i := 0; i < (MaxTotalImageBytes/MaxImageBytes)+1; i++ {
			images = append(images, Image{MIME: "image/png", Data: make([]byte, MaxImageBytes)})
		}
		if err := ValidateImages(images); err == nil {
			t.Fatal("images totalling over the payload limit must be rejected")
		}
		if err := ValidateImages(images[:1]); err != nil {
			t.Fatalf("one in-limit image must pass: %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if _, err := NewImage("empty.png", nil); err == nil {
			t.Fatal("an empty attachment must be rejected rather than sent")
		}
	})
}

// TestImageBytesNeverReachLogs — a []byte inside a struct prints every byte
// under %v, so one logged request would write the whole image into the log
// file, where it is neither redacted nor wanted.
func TestImageBytesNeverReachLogs(t *testing.T) {
	img, err := NewImage("shot.png", pngBytes(t))
	if err != nil {
		t.Fatalf("new image: %v", err)
	}
	// The verbs a logger actually reaches for, plus the message the image is
	// carried in — slog on a request struct renders it the same way.
	rendered := fmt.Sprintf("%s %v %s %v",
		img.String(), img, img,
		Message{Role: RoleUser, Content: "look", Images: []Image{img}})

	if strings.Contains(rendered, string(img.Data[:8])) {
		t.Fatalf("raw image bytes reached a formatted string: %q", rendered)
	}
	if run := regexp.MustCompile(`[A-Za-z0-9+/]{100,}`).FindString(rendered); run != "" {
		t.Fatalf("a base64-looking run reached a formatted string: %q", run)
	}
	if !strings.Contains(rendered, "image/png") {
		t.Fatalf("the rendering must still say what it is: %q", rendered)
	}
}

// TestVisionGateRunsBeforeTheNetwork — the whole point of the capability check
// is that an image never leaves the machine when no configured model can read
// it. A provider that was dialled at all fails this.
func TestVisionGateRunsBeforeTheNetwork(t *testing.T) {
	img, err := NewImage("shot.png", pngBytes(t))
	if err != nil {
		t.Fatalf("new image: %v", err)
	}

	textOnly := &mockProvider{name: "text-only", chatResponse: &ChatResponse{ID: "resp"}}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "text-only"})
	mp.Register("text-only", textOnly, "openai/gpt-3.5-turbo", 1)

	_, err = mp.Chat(context.Background(), ChatRequest{
		Model:    "text-only",
		Messages: []Message{{Role: RoleUser, Content: "what is this?", Images: []Image{img}}},
	})
	if err == nil {
		t.Fatal("an image sent to a text-only model must fail")
	}
	if textOnly.chatCalls != 0 {
		t.Fatal("the provider was dialled: the image left the machine before the check")
	}
	var unsupported *ErrVisionUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("want ErrVisionUnsupported, got %T: %v", err, err)
	}
	for _, want := range []string{"gpt-3.5-turbo", "gpt-4o"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must name the model tried and suggest a working one, missing %q in %v", want, err)
		}
	}

	// A vision-capable model in the chain is used rather than refused. The
	// text-only entry stays registered: the gate must drop it from the chain,
	// not merely notice that something capable exists.
	vision := &mockProvider{name: "vision", chatResponse: &ChatResponse{ID: "resp"}}
	mp.Register("vision", vision, "openai/gpt-4o", 2)
	if _, err := mp.Chat(context.Background(), ChatRequest{
		Model:    "vision",
		Messages: []Message{{Role: RoleUser, Content: "what is this?", Images: []Image{img}}},
	}); err != nil {
		t.Fatalf("a vision-capable model must accept the image: %v", err)
	}
	if textOnly.chatCalls != 0 {
		t.Fatal("the text-only provider was still in the fallback chain")
	}
	if vision.chatCalls != 1 {
		t.Fatalf("the vision provider was dialled %d times, want 1", vision.chatCalls)
	}
}

// TestUnknownModelIsNotVisionCapable — failing closed is deliberate. Guessing
// "yes" turns a typo into a provider 400 mid-conversation.
func TestUnknownModelIsNotVisionCapable(t *testing.T) {
	for _, spec := range []string{"", "some-model-nobody-has-heard-of", "openrouter/acme/mystery-1"} {
		if SupportsVision(spec) {
			t.Fatalf("%q must not be treated as vision-capable", spec)
		}
	}
	// Marked models are recognised under every name shape they arrive in.
	for _, spec := range []string{
		"gpt-4o", "openai/gpt-4o", "openrouter/openai/gpt-4o",
		"anthropic/claude-sonnet-4-20250514", "ollama/llava:13b", "google/gemini-2.5-pro",
	} {
		if !SupportsVision(spec) {
			t.Fatalf("%q is documented as vision-capable but was not recognised", spec)
		}
	}
}
