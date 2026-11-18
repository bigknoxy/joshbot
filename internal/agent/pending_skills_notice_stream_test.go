package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// A provider that answers with a fixed sentence.
func noticeAnsweringProvider() *mockProvider {
	return &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{Choices: []providers.Choice{{
				Message: providers.Message{Role: providers.RoleAssistant, Content: "the answer"},
			}}}, nil
		},
	}
}

// TestPendingSkillsNoticeReachesTheStreamSink is the bug the live TTY
// showed: when the turn streams, the answer reaches the user through the
// stream sink and the returned text is never printed, so a notice carried
// only in the return value was never seen by anyone. The notice has to go
// through the sink with the answer.
func TestPendingSkillsNoticeReachesTheStreamSink(t *testing.T) {
	workspace := t.TempDir()
	writeSkill(t, workspace, "alpha", "alpha")

	loader := pendingSkillLoader(t, workspace)

	var streamed strings.Builder
	ctx := WithStreamSink(context.Background(), func(ev StreamEvent) {
		streamed.WriteString(ev.Delta)
	})

	a := NewAgent(newNonStreamingConfig(), noticeAnsweringProvider(), &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	a.skillLoader = loader

	got, err := a.Process(ctx, bus.InboundMessage{
		SenderID: "u", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !strings.Contains(streamed.String(), "alpha") {
		t.Errorf("the pending skill must be named through the stream sink, got:\n%s", streamed.String())
	}
	if !strings.Contains(streamed.String(), "joshbot skills trust") {
		t.Errorf("the notice must say what to do about it, got:\n%s", streamed.String())
	}
	// It must not be in the reply as well: a consumer that prints both the
	// sink and the reply would show it twice.
	if strings.Contains(got, "alpha") {
		t.Errorf("the notice must not be carried twice, reply = %q", got)
	}
}

// TestPendingSkillsNoticeStaysInTheReplyWithoutASink: the non-streaming
// consumer prints the returned text, so that is where the notice belongs.
func TestPendingSkillsNoticeStaysInTheReplyWithoutASink(t *testing.T) {
	workspace := t.TempDir()
	writeSkill(t, workspace, "alpha", "alpha")

	loader := pendingSkillLoader(t, workspace)

	a := NewAgent(newNonStreamingConfig(), noticeAnsweringProvider(), &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	a.skillLoader = loader

	got, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "u", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !strings.Contains(got, "alpha") {
		t.Errorf("the pending skill must be named in the reply, got: %q", got)
	}
	if !strings.Contains(got, "the answer") {
		t.Errorf("the answer must survive, got: %q", got)
	}
}
