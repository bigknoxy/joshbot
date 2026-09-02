package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

// This file tests the cheap, no-LLM-call tier that runs ahead of
// checkAndCompactContext's expensive summarization call: maskStaleToolOutput
// truncates old tool/assistant content in place, the same way
// applyObservationMasking already does for buildMessages, but scoped to
// exactly the prefix compaction would otherwise summarize. If trimming alone
// gets the conversation back under budget, a full LLM compaction is not
// needed at all.

// seedToolHeavyHistory fills a session with `exchanges` tool call/result pairs
// of `resultSize` bytes each. Tool and assistant content dominates the token
// count here on purpose — masking only ever touches those two roles, so a
// scenario dominated by user text would never let cheap masking succeed and
// would silently prove nothing.
func seedToolHeavyHistory(t *testing.T, sessions SessionManager, key string, exchanges, resultSize int) *session.Session {
	t.Helper()
	sess, err := sessions.GetOrCreate(context.Background(), key)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sess.AddMessage(session.Message{Role: session.RoleUser, Content: "start"})
	for i := 0; i < exchanges; i++ {
		for _, m := range toolExchange(fmt.Sprintf("h%d", i), "noop", padding(resultSize)) {
			sess.AddMessage(m)
		}
	}
	if err := sessions.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return sess
}

// The headline behaviour: when old tool output alone is what pushed the
// conversation over threshold, trimming it must be enough to avoid the LLM
// compression call entirely.
func TestCheapMaskingAvoidsFullCompactionWhenTrimmingIsEnough(t *testing.T) {
	sessions := newMockSessionManager()
	// Sized to land between checkAndCompactContext's threshold budget and
	// buildMessages' own full-budget masking trigger: over threshold once the
	// tool round appends a couple more tokens, but under the full budget, so
	// buildMessages's pre-existing masking never fires and this test actually
	// exercises checkAndCompactContext's own cheap tier, not a different one.
	seedToolHeavyHistory(t, sessions, "cli:eval", 18, 700)

	comp := &countingCompressor{}
	f := newCompactionFixture(t, sessions, comp, toolTurns(1))
	f.turn(t, "first")

	if got := comp.count(); got != 0 {
		t.Fatalf("full compaction ran %d times; cheap masking of the old tool output alone should have gotten the prefix under budget", got)
	}
	if _, ok := f.session(t).CompactionRecord(); ok {
		t.Error("no LLM compaction happened, so the session must not carry a compaction record")
	}

	reqs := f.provider.requests
	if len(reqs) == 0 {
		t.Fatal("no provider requests recorded")
	}
	var sawTruncated bool
	for _, req := range reqs {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "[Tool output truncated]") {
				sawTruncated = true
			}
		}
	}
	if !sawTruncated {
		t.Error("expected old tool output to be masked/truncated in at least one request, but none was found")
	}
}

// Masking must never reach into the live turn: the exchange the user is in
// the middle of has to arrive at the model exactly as the tool produced it,
// for the same reason full compaction never summarizes it (#346).
func TestCheapMaskingNeverTouchesTheLiveTurn(t *testing.T) {
	sessions := newMockSessionManager()
	seedToolHeavyHistory(t, sessions, "cli:eval", 20, 700)

	comp := &countingCompressor{}
	liveResult := "LIVE-MARKER-" + strings.Repeat("y", 300)
	toolLayer := &recordingTools{outputs: map[string]string{"noop": liveResult}}
	f := newCompactionFixture(t, sessions, comp, toolTurns(1))
	f.agent.tools = toolLayer
	f.turn(t, "first")

	if got := comp.count(); got != 0 {
		t.Fatalf("setup: expected cheap masking to avoid full compaction so this test exercises the masking path, but the compressor ran %d times", got)
	}

	reqs := f.provider.requests
	if len(reqs) == 0 {
		t.Fatal("no provider requests recorded")
	}
	last := reqs[len(reqs)-1]
	var sawLive bool
	for _, m := range last.Messages {
		if m.Content == liveResult {
			sawLive = true
		}
	}
	if !sawLive {
		t.Error("the live turn's own tool output was masked/truncated; only the prefix before it should ever be touched")
	}
}

// When trimming alone is not enough, the existing full-compaction path must
// still run exactly as before — this is a regression guard, not new
// behaviour, using the same shape the pre-existing compaction tests rely on
// (large, symmetric user+assistant history that masking cannot shrink enough
// because half of it is user content, which is never masked).
func TestCheapMaskingInsufficientFallsThroughToFullCompaction(t *testing.T) {
	sessions := newMockSessionManager()
	seedHistory(t, sessions, "cli:eval", 30)

	comp := &countingCompressor{}
	f := newCompactionFixture(t, sessions, comp, toolTurns(8))
	f.turn(t, "first")

	if got := comp.count(); got != 1 {
		t.Fatalf("expected exactly 1 full compaction when cheap masking cannot get under budget, got %d", got)
	}
	if _, ok := f.session(t).CompactionRecord(); !ok {
		t.Error("expected a compaction record after the full compaction ran")
	}
}

// --- unit tests for the pure masking function -------------------------------

func maskingFixtureMessages() []providers.Message {
	return []providers.Message{
		{Role: providers.RoleSystem, Content: "sys"},
		{Role: providers.RoleUser, Content: "u0"},
		{Role: providers.RoleAssistant, Content: strings.Repeat("a", 500)},
		{Role: providers.RoleTool, Content: strings.Repeat("b", 500), ToolCallID: "t1"},
		{Role: providers.RoleUser, Content: "u1"},
		{Role: providers.RoleAssistant, Content: strings.Repeat("c", 500)},
		{Role: providers.RoleTool, Content: strings.Repeat("d", 500), ToolCallID: "t2"},
	}
}

// sumTokens mirrors checkAndCompactContext's own accounting: the caller's
// totalTokens excludes the system message at index 0.
func sumTokens(messages []providers.Message) int {
	total := 0
	for i := 1; i < len(messages); i++ {
		total += ctxpkg.TokenEstimator(messages[i].Content)
	}
	return total
}

func TestMaskStaleToolOutputMasksOnlyThePrefix(t *testing.T) {
	msgs := maskingFixtureMessages()

	masked, ok := maskStaleToolOutput(msgs, 2, 100000, sumTokens(msgs))
	if !ok {
		t.Fatal("expected masking to report success with a generous budget")
	}

	if masked[0].Content != "sys" {
		t.Errorf("system message must never be masked, got %q", masked[0].Content)
	}
	if masked[1].Content != "u0" || masked[4].Content != "u1" {
		t.Error("user messages must never be masked")
	}
	if strings.Contains(masked[2].Content, "[Tool output truncated]") == false {
		t.Errorf("expected the old assistant message to be truncated, got %q", masked[2].Content)
	}
	if strings.Contains(masked[3].Content, "[Tool output truncated]") == false {
		t.Errorf("expected the old tool message to be truncated, got %q", masked[3].Content)
	}
	if masked[3].ToolCallID != "t1" {
		t.Error("masking must preserve ToolCallID, or the provider rejects the request")
	}

	// The protected tail (last 2 messages) must survive byte-for-byte.
	if masked[5].Content != strings.Repeat("c", 500) {
		t.Error("protected tail assistant message was masked")
	}
	if masked[6].Content != strings.Repeat("d", 500) {
		t.Error("protected tail tool message was masked")
	}
	if masked[6].ToolCallID != "t2" {
		t.Error("protected tail ToolCallID was altered")
	}
}

func TestMaskStaleToolOutputReportsFailureWhenStillOverBudget(t *testing.T) {
	msgs := maskingFixtureMessages()

	masked, ok := maskStaleToolOutput(msgs, 2, 1, sumTokens(msgs))
	if ok {
		t.Fatalf("expected masking to report failure against a 1-token budget, got success with %d messages", len(masked))
	}
	if masked != nil {
		t.Error("a failed masking attempt must return nil so the caller cannot mistake it for a usable result")
	}
}

func TestMaskStaleToolOutputProtectsEverythingWhenTailCoversTheWholeSlice(t *testing.T) {
	msgs := maskingFixtureMessages()

	// protectTail >= len(msgs) must never mask anything but the caller's
	// budget check will then correctly still report failure — there was
	// nothing to trim.
	masked, ok := maskStaleToolOutput(msgs, len(msgs)+5, 1, sumTokens(msgs))
	if ok {
		t.Fatal("nothing was masked, so success against a 1-token budget is impossible")
	}
	_ = masked
}

func TestMaskStaleToolOutputIsIdempotent(t *testing.T) {
	msgs := maskingFixtureMessages()

	once, ok := maskStaleToolOutput(msgs, 2, 100000, sumTokens(msgs))
	if !ok {
		t.Fatal("setup: first pass must succeed")
	}
	twice, ok := maskStaleToolOutput(once, 2, 100000, sumTokens(once))
	if !ok {
		t.Fatal("setup: second pass must succeed")
	}
	for i := range once {
		if once[i].Content != twice[i].Content {
			t.Errorf("message %d changed on a second masking pass: %q -> %q", i, once[i].Content, twice[i].Content)
		}
	}
}
