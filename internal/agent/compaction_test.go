package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

// countingCompressor records how often compression actually ran. That count is
// the whole point of issue #125: before the fix it grew by one on every turn
// past the threshold, forever.
type countingCompressor struct {
	mu      sync.Mutex
	calls   int
	err     error
	summary string
}

func (c *countingCompressor) CompressMessages(_ context.Context, _ string, msgs []providers.Message, _ int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return "", c.err
	}
	if c.summary != "" {
		return c.summary, nil
	}
	return "SUMMARY of " + strings.Repeat("z", 20), nil
}

func (c *countingCompressor) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// toolTurns scripts n Process calls that each make one tool call and then
// answer.
//
// The tool call matters: checkAndCompactContext runs only after tool execution
// inside the ReAct loop, so a turn that answers directly never compacts at all.
// A fixture scripted without tool calls silently exercises nothing.
func toolTurns(n int) []scriptedTurn {
	ts := make([]scriptedTurn, 0, n*2)
	for i := 0; i < n; i++ {
		ts = append(ts,
			scriptedTurn{toolCalls: []providers.ToolCall{toolCall(fmt.Sprintf("c%d", i), "noop", `{}`)}},
			scriptedTurn{content: "done"},
		)
	}
	return ts
}

// compactionFixture builds an agent wired for compaction with a tight budget,
// so any non-trivial history exceeds it.
type compactionFixture struct {
	agent      *Agent
	sessions   SessionManager
	compressor *countingCompressor
	provider   *scriptedProvider
	sessionKey string
}

func newCompactionFixture(t *testing.T, sessions SessionManager, comp *countingCompressor, turns []scriptedTurn) *compactionFixture {
	t.Helper()

	InvalidatePromptCache()

	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Model = "small"
	cfg.Agents.Defaults.MaxTokens = 100

	provider := &scriptedProvider{turns: turns}
	toolLayer := &recordingTools{outputs: map[string]string{}}

	a := NewAgent(cfg, provider, toolLayer, sessions, newMockLogger(),
		// Margin 3800 against the 4096-token "small" window drives the budget
		// to its floor, so seeded history always exceeds it.
		WithBudgetManager(ctxpkg.NewBudgetManager(ctxpkg.NewRegistry(), 3800)),
		WithContextCompressor(comp),
	)

	return &compactionFixture{
		agent:      a,
		sessions:   sessions,
		compressor: comp,
		provider:   provider,
		sessionKey: "cli:eval",
	}
}

func (f *compactionFixture) turn(t *testing.T, content string) {
	t.Helper()
	if _, err := f.agent.Process(context.Background(), bus.InboundMessage{
		Channel:  "cli",
		SenderID: "eval",
		Content:  content,
	}); err != nil {
		t.Fatalf("Process: %v", err)
	}
}

func (f *compactionFixture) session(t *testing.T) *session.Session {
	t.Helper()
	sess, err := f.sessions.GetOrCreate(context.Background(), f.sessionKey)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	return sess
}

// seedHistory fills a session with enough content to blow the budget.
func seedHistory(t *testing.T, sessions SessionManager, key string, pairs int) *session.Session {
	t.Helper()
	sess, err := sessions.GetOrCreate(context.Background(), key)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	for _, m := range chatHistory(pairs, 400) {
		sess.AddMessage(m)
	}
	if err := sessions.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return sess
}

// The regression test for #125: compaction must happen once, not once per turn.
func TestCompactionRunsOnceAcrossTurns(t *testing.T) {
	sessions := newMockSessionManager()
	seedHistory(t, sessions, "cli:eval", 30)

	comp := &countingCompressor{}
	f := newCompactionFixture(t, sessions, comp, toolTurns(8))

	f.turn(t, "first")
	afterFirst := comp.count()
	if afterFirst != 1 {
		t.Fatalf("expected exactly 1 compression on the turn that crosses the threshold, got %d", afterFirst)
	}

	// Three more turns. The stored summary is small, so the history stays under
	// the threshold and nothing should be recompressed.
	f.turn(t, "second")
	f.turn(t, "third")
	f.turn(t, "fourth")

	if got := comp.count(); got != 1 {
		t.Errorf("compression ran %d times across 4 turns; want 1 — the summary is being recomputed every turn (#125)", got)
	}
}

// The summary must actually be in the session, not only in the provider slice.
func TestCompactionRecordIsStoredInSession(t *testing.T) {
	sessions := newMockSessionManager()
	seedHistory(t, sessions, "cli:eval", 30)

	comp := &countingCompressor{summary: "the conversation so far"}
	f := newCompactionFixture(t, sessions, comp, toolTurns(8))
	f.turn(t, "first")

	sess := f.session(t)
	rec, ok := sess.CompactionRecord()
	if !ok {
		t.Fatal("expected a compaction record in the session")
	}
	if !strings.Contains(rec.Content, "the conversation so far") {
		t.Errorf("compaction record does not carry the summary: %q", rec.Content)
	}
	if !rec.Compaction {
		t.Error("record is not marked as a compaction")
	}
	if got := sess.CountCompactionRecords(); got != 1 {
		t.Errorf("expected exactly 1 compaction record, got %d", got)
	}
}

// A second compaction subsumes the first rather than accumulating records.
func TestSecondCompactionReplacesTheFirst(t *testing.T) {
	sessions := newMockSessionManager()
	seedHistory(t, sessions, "cli:eval", 30)

	comp := &countingCompressor{}
	f := newCompactionFixture(t, sessions, comp, toolTurns(8))
	f.turn(t, "first")

	if got := f.session(t).CountCompactionRecords(); got != 1 {
		t.Fatalf("after first compaction: got %d records, want 1", got)
	}

	// Push the session back over the threshold with fresh bulk, then run a turn.
	sess := f.session(t)
	for _, m := range chatHistory(30, 400) {
		sess.AddMessage(m)
	}
	f.turn(t, "second")

	if got := comp.count(); got != 2 {
		t.Fatalf("expected a second compression after re-crossing the threshold, got %d calls", got)
	}
	if got := f.session(t).CountCompactionRecords(); got != 1 {
		t.Errorf("compaction records accumulated: got %d, want 1", got)
	}
	if !f.session(t).Messages[0].Compaction {
		t.Error("the compaction record must remain at index 0")
	}
}

// A failed compression must leave the session exactly as it was.
func TestFailedCompactionLeavesSessionUnmodified(t *testing.T) {
	sessions := newMockSessionManager()
	seedHistory(t, sessions, "cli:eval", 30)

	comp := &countingCompressor{err: errors.New("provider exploded")}
	f := newCompactionFixture(t, sessions, comp, toolTurns(8))

	before := append([]session.Message(nil), f.session(t).Messages...)
	f.turn(t, "first")

	after := f.session(t).Messages
	if got := f.session(t).CountCompactionRecords(); got != 0 {
		t.Fatalf("a failed compression must not write a record, got %d", got)
	}
	// The turn legitimately appends its own user/assistant messages; everything
	// that existed before must still be there, in order, untouched.
	if len(after) < len(before) {
		t.Fatalf("session shrank on a failed compression: %d -> %d", len(before), len(after))
	}
	for i, m := range before {
		if after[i].Content != m.Content || after[i].Role != m.Role {
			t.Fatalf("message %d changed on a failed compression: %+v -> %+v", i, m, after[i])
		}
	}
}

// Compaction must not fire repeatedly within a single turn once the context is
// back under the threshold.
func TestCompactionNotRepeatedWithinOneTurn(t *testing.T) {
	sessions := newMockSessionManager()
	seedHistory(t, sessions, "cli:eval", 30)

	comp := &countingCompressor{}
	// Three tool-calling iterations, then a plain answer.
	turns := []scriptedTurn{
		{toolCalls: []providers.ToolCall{toolCall("c1", "noop", `{}`)}},
		{toolCalls: []providers.ToolCall{toolCall("c2", "noop", `{}`)}},
		{toolCalls: []providers.ToolCall{toolCall("c3", "noop", `{}`)}},
		{content: "done"},
	}
	f := newCompactionFixture(t, sessions, comp, turns)
	f.turn(t, "first")

	if got := comp.count(); got > 1 {
		t.Errorf("compression ran %d times in one turn; the context was already under budget after the first", got)
	}
}

// The stored record must survive the memory-window slide. Dropping it would
// silently discard the entire earlier conversation.
func TestCompactionRecordSurvivesMemoryWindow(t *testing.T) {
	sessions := newMockSessionManager()
	seedHistory(t, sessions, "cli:eval", 30)

	comp := &countingCompressor{summary: "durable summary marker"}
	f := newCompactionFixture(t, sessions, comp, toolTurns(8))

	// The window is applied only after the record exists. Setting it first would
	// truncate the seeded history small enough that nothing ever compacts, and
	// the test would pass vacuously.
	f.turn(t, "first")
	if _, ok := f.session(t).CompactionRecord(); !ok {
		t.Fatal("setup: expected the first turn to produce a compaction record")
	}
	f.agent.cfg.Agents.Defaults.MemoryWindow = 2

	// Add more than the window's worth of new messages, then take another turn.
	sess := f.session(t)
	for _, m := range chatHistory(6, 10) {
		sess.AddMessage(m)
	}
	f.turn(t, "second")

	// Find the request for the last turn and assert the summary was still sent.
	reqs := f.provider.requests
	if len(reqs) == 0 {
		t.Fatal("no provider requests recorded")
	}
	last := reqs[len(reqs)-1]
	var found bool
	for _, m := range last.Messages {
		if strings.Contains(m.Content, "durable summary marker") {
			found = true
		}
	}
	if !found {
		t.Error("the compaction record was dropped by the memory window — the earlier conversation is lost")
	}
}

// A compacted history must never open with a tool result whose announcing
// assistant message was summarized away; providers reject that with a 400.
func TestCompactedHistoryHasNoOrphanedToolResults(t *testing.T) {
	sessions := newMockSessionManager()
	sess, err := sessions.GetOrCreate(context.Background(), "cli:eval")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	for _, m := range chatHistory(20, 400) {
		sess.AddMessage(m)
	}
	// Deliberately leave a tool exchange at the tail.
	for _, m := range toolExchange("t1", "noop", "ok") {
		sess.AddMessage(m)
	}

	comp := &countingCompressor{}
	f := newCompactionFixture(t, sessions, comp, toolTurns(8))
	f.turn(t, "first")

	// Force a stored record with a tail that starts on a tool message.
	stored := f.session(t)
	if _, ok := stored.CompactionRecord(); !ok {
		t.Skip("no compaction occurred; nothing to assert")
	}
	stored.Messages = append(stored.Messages[:1], session.Message{
		Role:       session.RoleTool,
		Content:    "orphan",
		ToolCallID: "gone",
	})

	built := f.agent.buildMessages("system", stored)
	for i, m := range built {
		if m.Role == providers.RoleTool && i > 0 && built[i-1].Role != providers.RoleAssistant {
			t.Fatalf("orphaned tool result at index %d survived into the request", i)
		}
	}
}

// --- persistence, via the real session.Manager -----------------------------

// Compaction must shrink the live session file, and must not destroy the
// messages it replaced.
func TestCompactionShrinksSessionFileAndArchivesHistory(t *testing.T) {
	dir := t.TempDir()
	mgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	sess := seedHistory(t, mgr, "cli:eval", 30)
	originalCount := len(sess.Messages)

	sizeBefore := sessionFileSize(t, dir, "cli:eval")

	comp := &countingCompressor{}
	f := newCompactionFixture(t, mgr, comp, toolTurns(8))
	f.turn(t, "first")

	sizeAfter := sessionFileSize(t, dir, "cli:eval")
	if sizeAfter >= sizeBefore {
		t.Errorf("session file did not shrink after compaction: %d -> %d bytes", sizeBefore, sizeAfter)
	}

	// The replaced messages are preserved, not deleted.
	archive, err := os.ReadFile(dir + "/cli:eval.history.jsonl")
	if err != nil {
		t.Fatalf("expected an archive of the compacted messages: %v", err)
	}
	archivedLines := strings.Count(strings.TrimSpace(string(archive)), "\n") + 1
	if archivedLines < originalCount {
		t.Errorf("archive holds %d messages, expected at least the %d that were summarized", archivedLines, originalCount)
	}
}

// The record must round-trip through disk unchanged, or the next turn
// recomputes it anyway.
func TestCompactionRecordSurvivesSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	seedHistory(t, mgr, "cli:eval", 30)

	comp := &countingCompressor{summary: "round trip me"}
	f := newCompactionFixture(t, mgr, comp, toolTurns(8))
	f.turn(t, "first")

	inMemory, ok := f.session(t).CompactionRecord()
	if !ok {
		t.Fatal("no compaction record after the turn")
	}

	reloadMgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	reloaded, err := reloadMgr.Load(context.Background(), "cli:eval")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	onDisk, ok := reloaded.CompactionRecord()
	if !ok {
		t.Fatal("the compaction record did not survive the round trip")
	}
	if onDisk.Content != inMemory.Content {
		t.Errorf("record content changed across the round trip:\n in-memory: %q\n on-disk:   %q", inMemory.Content, onDisk.Content)
	}
	if !onDisk.Compaction {
		t.Error("the compaction flag did not survive the round trip")
	}
}

// If the history cannot be archived, the compaction is not applied — losing a
// conversation is worse than recomputing a summary.
func TestCompactionSkippedWhenArchiveFails(t *testing.T) {
	sessions := &failingArchiveSessions{mockSessionManager: newMockSessionManager()}
	seedHistory(t, sessions, "cli:eval", 30)

	comp := &countingCompressor{}
	f := newCompactionFixture(t, sessions, comp, toolTurns(8))

	before := len(f.session(t).Messages)
	f.turn(t, "first")

	if got := f.session(t).CountCompactionRecords(); got != 0 {
		t.Errorf("compaction was applied despite a failed archive: %d records", got)
	}
	if got := len(f.session(t).Messages); got < before {
		t.Errorf("session shrank despite a failed archive: %d -> %d", before, got)
	}
}

type failingArchiveSessions struct {
	*mockSessionManager
}

func (f *failingArchiveSessions) Archive(_ context.Context, _ string, _ []session.Message) error {
	return errors.New("disk full")
}

func sessionFileSize(t *testing.T, dir, id string) int64 {
	t.Helper()
	info, err := os.Stat(dir + "/" + id + ".jsonl")
	if err != nil {
		t.Fatalf("stat session file: %v", err)
	}
	return info.Size()
}
