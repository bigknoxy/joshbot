package providers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// dialCountingProvider records both dial paths, which is what lets a test prove the
// document gate ran before the network on the streaming path as well as the
// non-streaming one.
type dialCountingProvider struct {
	name        string
	chatCalls   int
	streamCalls int
}

func (c *dialCountingProvider) Chat(context.Context, ChatRequest) (*ChatResponse, error) {
	c.chatCalls++
	return &ChatResponse{ID: "resp"}, nil
}

func (c *dialCountingProvider) ChatStream(context.Context, ChatRequest) (<-chan StreamChunk, error) {
	c.streamCalls++
	ch := make(chan StreamChunk)
	close(ch)
	return ch, nil
}

func (c *dialCountingProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", nil
}
func (c *dialCountingProvider) Name() string   { return c.name }
func (c *dialCountingProvider) Config() Config { return Config{} }

func pdfRequest(model string) ChatRequest {
	return ChatRequest{Model: model, Messages: []Message{{
		Role:      RoleUser,
		Content:   "summarise this",
		Documents: []Document{{Label: "report.pdf", MIME: MIMEPDF, Data: []byte("%PDF-1.7\nbody")}},
	}}}
}

// TestDocumentGateRunsBeforeTheNetworkOnBothPaths is the central safety case.
// The PDF must never leave the machine when no configured model can read it,
// and the check has to be wired into ChatStream as well as Chat — a gate
// present on only one path is a live bug class in this codebase.
func TestDocumentGateRunsBeforeTheNetworkOnBothPaths(t *testing.T) {
	for _, path := range []string{"chat", "chatstream"} {
		t.Run(path, func(t *testing.T) {
			p := &dialCountingProvider{name: "text-only"}
			mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "text-only"})
			mp.Register("text-only", p, "ollama/llava", 1)

			var err error
			if path == "chat" {
				_, err = mp.Chat(context.Background(), pdfRequest("text-only"))
			} else {
				_, err = mp.ChatStream(context.Background(), pdfRequest("text-only"))
			}
			if err == nil {
				t.Fatal("a PDF sent to a model that cannot read documents must fail")
			}
			if p.chatCalls != 0 || p.streamCalls != 0 {
				t.Fatalf("the provider was dialled (chat=%d stream=%d): the PDF left the machine before the check",
					p.chatCalls, p.streamCalls)
			}
			var unsupported *ErrDocumentsUnsupported
			if !errors.As(err, &unsupported) {
				t.Fatalf("want ErrDocumentsUnsupported, got %T: %v", err, err)
			}
			// Acceptance: the refusal names the model.
			if !strings.Contains(err.Error(), "ollama/llava") {
				t.Fatalf("the refusal must name the model, got %v", err)
			}
		})
	}
}

// TestDocumentGateKeepsCapableProvidersOnBothPaths is the other half: a capable
// model is dialled rather than refused, and an incapable entry is dropped from
// the chain rather than merely noticed.
func TestDocumentGateKeepsCapableProvidersOnBothPaths(t *testing.T) {
	for _, path := range []string{"chat", "chatstream"} {
		t.Run(path, func(t *testing.T) {
			textOnly := &dialCountingProvider{name: "text-only"}
			capable := &dialCountingProvider{name: "capable"}
			mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "capable"})
			mp.Register("text-only", textOnly, "ollama/llava", 2)
			mp.Register("capable", capable, "openai/gpt-4o", 1)

			var err error
			if path == "chat" {
				_, err = mp.Chat(context.Background(), pdfRequest("capable"))
			} else {
				_, err = mp.ChatStream(context.Background(), pdfRequest("capable"))
			}
			if err != nil {
				t.Fatalf("a document-capable model must accept the PDF: %v", err)
			}
			if textOnly.chatCalls != 0 || textOnly.streamCalls != 0 {
				t.Fatal("the text-only provider was still in the fallback chain")
			}
			if capable.chatCalls+capable.streamCalls != 1 {
				t.Fatalf("the capable provider was dialled %d times, want 1",
					capable.chatCalls+capable.streamCalls)
			}
		})
	}
}

// TestDocumentTotalCapIsEnforcedBeforeAnyDial — a limit reported after the
// upstream call has already been billed has not limited anything.
func TestDocumentTotalCapIsEnforcedBeforeAnyDial(t *testing.T) {
	p := &dialCountingProvider{name: "capable"}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "capable"})
	mp.Register("capable", p, "openai/gpt-4o", 1)

	big := append([]byte("%PDF-1.7\n"), make([]byte, MaxTotalDocumentBytes)...)
	req := ChatRequest{Model: "capable", Messages: []Message{{
		Role:      RoleUser,
		Documents: []Document{{Label: "a.pdf", MIME: MIMEPDF, Data: big}},
	}}}
	if _, err := mp.Chat(context.Background(), req); err == nil {
		t.Fatal("an over-total-limit document was accepted")
	}
	if p.chatCalls != 0 {
		t.Fatal("the provider was dialled before the size check")
	}
}

// TestTextOnlyRequestIsUnaffectedByTheDocumentGate — the gate must be inert for
// every ordinary turn.
func TestTextOnlyRequestIsUnaffectedByTheDocumentGate(t *testing.T) {
	p := &dialCountingProvider{name: "plain"}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "plain"})
	mp.Register("plain", p, "some-unknown-text-model", 1)

	if _, err := mp.Chat(context.Background(), ChatRequest{
		Model:    "plain",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("a text-only turn must not be screened for documents: %v", err)
	}
	if p.chatCalls != 1 {
		t.Fatalf("chatCalls = %d, want 1", p.chatCalls)
	}
}
