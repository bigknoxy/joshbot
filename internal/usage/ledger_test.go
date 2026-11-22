package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

func TestLedgerRecordsAndSpends(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	l, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}

	// One priced call and one unpriced call, same session.
	l.RecordUsage("cli:alice", "claude-sonnet-4.5", providers.Usage{PromptTokens: 1_000_000, CompletionTokens: 500_000})
	l.RecordUsage("cli:alice", "z-ai/glm-5.2", providers.Usage{PromptTokens: 1_000_000, CompletionTokens: 0})

	// 1M input ($3) + 0.5M output ($7.50) = $10.50; the unpriced call adds
	// tokens but no cost, and that must be visible, not folded into zero.
	if s, total := l.Spend("cli:alice"); s != 10.50 || total != 10.50 {
		t.Errorf("Spend = (%f, %f), want (10.50, 10.50)", s, total)
	}
	if s, _ := l.Spend("cli:other"); s != 0 {
		t.Errorf("unknown session Spend = %f, want 0", s)
	}
	if l.Total.PromptTokens != 2_000_000 || l.Total.CompletionTokens != 500_000 || l.Total.Calls != 2 {
		t.Errorf("lifetime totals wrong: %+v", l.Total)
	}
	if l.Total.UnknownCostCalls != 1 {
		t.Errorf("UnknownCostCalls = %d, want 1", l.Total.UnknownCostCalls)
	}
	if _, ok := l.Models["claude-sonnet-4.5"]; !ok {
		t.Errorf("per-model breakdown missing claude-sonnet-4.5: %+v", l.Models)
	}
}

func TestLedgerSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	l1, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	l1.RecordUsage("telegram:42", "claude-haiku-4", providers.Usage{PromptTokens: 4000, CompletionTokens: 1000})

	l2, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	want := 4000.0/1e6*1.00 + 1000.0/1e6*5.00 // $0.004 + $0.005 = $0.009
	if got, _ := l2.Spend("telegram:42"); got < want-1e-9 || got > want+1e-9 {
		t.Errorf("spend after reload = %f, want ~%f; the ledger must survive a restart", got, want)
	}
	if l2.Total.Calls != 1 || l2.Total.TotalTokens != 5000 {
		t.Errorf("totals after reload wrong: %+v", l2.Total)
	}
	if _, ok := l2.Models["claude-haiku-4"]; !ok {
		t.Errorf("per-model breakdown lost on reload: %+v", l2.Models)
	}
	// The reload must not reset the started-at stamp to "now".
	if l2.StartedAt.IsZero() || !l2.StartedAt.Equal(l1.StartedAt) {
		t.Errorf("StartedAt not carried over: %v (was %v)", l2.StartedAt, l1.StartedAt)
	}
}

func TestLedgerUnreadableFileStartsFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{not json"), fileMode); err != nil {
		t.Fatal(err)
	}
	l, err := NewLedger(path)
	if err != nil {
		// Lenient on purpose: refusing to run because a report is broken.
		t.Fatalf("unparseable ledger must not be an error: %v", err)
	}
	if l.Total.Calls != 0 {
		t.Errorf("expected a fresh ledger, got %+v", l.Total)
	}
	l.RecordUsage("s", "gpt-4o", providers.Usage{PromptTokens: 10, CompletionTokens: 5})
	l2, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if l2.Total.TotalTokens != 15 {
		t.Errorf("fresh ledger did not take over: %+v", l2.Total)
	}
}

func TestLedgerFileIsOwnerOnlyValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	l, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	l.RecordUsage("s", "gpt-4o", providers.Usage{PromptTokens: 10, CompletionTokens: 5})
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("ledger mode = %o, want 600", perm)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f ledgerFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("ledger file not valid JSON: %v", err)
	}
	if f.Schema != 1 {
		t.Errorf("schema = %d, want 1", f.Schema)
	}
}

func TestLedgerEvictsStalestSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	l, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxSessions+5; i++ {
		l.RecordUsage(sessionName(i), "gpt-4o", providers.Usage{PromptTokens: 1, CompletionTokens: 1})
	}
	l.mu.Lock()
	n := len(l.Sessions)
	l.mu.Unlock()
	if n != maxSessions {
		t.Errorf("session count = %d, want cap %d", n, maxSessions)
	}
	// The stalest entries (the first five) are the evicted ones.
	for i := 0; i < 5; i++ {
		l.mu.Lock()
		_, ok := l.Sessions[sessionName(i)]
		l.mu.Unlock()
		if ok {
			t.Errorf("stalest session %s not evicted", sessionName(i))
		}
	}
	// Lifetime totals carry the whole unbounded volume regardless of the
	// per-session cap.
	if want := (maxSessions + 5) * 2; l.Total.TotalTokens != want {
		t.Errorf("lifetime tokens = %d, want %d (cap must not lose lifetime accounting)", l.Total.TotalTokens, want)
	}
}

func TestLedgerSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	l, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	l.RecordUsage("telegram:42", "gpt-4o", providers.Usage{PromptTokens: 1000, CompletionTokens: 500})
	l.RecordUsage("cli:boss", "gpt-4o", providers.Usage{PromptTokens: 10, CompletionTokens: 2})

	total, models, sessions := l.Snapshot()
	if total.Calls != 2 {
		t.Errorf("lifetime calls = %d, want 2", total.Calls)
	}
	if len(models) != 1 || models[0].Model != "gpt-4o" {
		t.Errorf("models = %+v, want one gpt-4o entry", models)
	}
	// cli:boss was seen last, so it leads the session list.
	if len(sessions) != 2 || sessions[0].Session != "cli:boss" {
		t.Errorf("sessions = %+v, want cli:boss first", sessions)
	}
}

// A burst of concurrent calls must not spend past the ceiling (#457). The
// reservation and the check that admitted the call happen together under the
// ledger's lock, so only the calls that fit in the ceiling are admitted —
// the recorded spend cannot pass the limit while calls are in flight.
func TestLedgerCeilingHoldsUnderConcurrentReservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	l, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}

	const ceiling = 1.00
	// One call holds $0.25 of the ceiling and costs $0.25: 25k output
	// tokens on gpt-4o at $10/M, with no prompt tokens.
	const calls = 50

	var admitted, refused atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := l.Reserve("gpt-4o", ceiling, 0, 25_000)
			if err != nil {
				refused.Add(1)
				return
			}
			admitted.Add(1)
			l.Commit(id, "cli:alice", "gpt-4o", providers.Usage{CompletionTokens: 25_000})
		}()
	}
	wg.Wait()

	if got := admitted.Load(); got != 4 {
		t.Errorf("admitted calls = %d, want 4 (the ceiling fits exactly four $0.25 calls)", got)
	}
	if got := refused.Load(); got != calls-4 {
		t.Errorf("refused calls = %d, want %d", got, calls-4)
	}
	if _, total := l.Spend("cli:alice"); total > ceiling {
		t.Errorf("recorded spend = $%.4f, want at most the $%.2f ceiling", total, ceiling)
	}

	// The file carries the same totals: concurrent saves must not publish
	// an older snapshot last.
	reloaded, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, total := reloaded.Spend("cli:alice"); total != 1.00 {
		t.Errorf("recorded spend on disk = $%.4f, want $1.00", total)
	}
}

// The ledger's disk write must not happen under the mutex that guards the
// accounting state (#457). The write is the slow part and every turn pays
// for it, so holding the lock across it would serialise the whole agent
// behind the slowest disk on the machine.
func TestLedgerWriteHappensOutsideTheStateLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	l, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}

	writing := make(chan struct{})
	release := make(chan struct{})
	l.persist = func(ledgerFile) error {
		close(writing)
		<-release
		return nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.RecordUsage("cli:alice", "gpt-4o", providers.Usage{PromptTokens: 10, CompletionTokens: 5})
	}()

	<-writing // the write is in flight

	read := make(chan struct{})
	go func() {
		defer close(read)
		l.Spend("cli:alice")
	}()
	select {
	case <-read:
	case <-time.After(10 * time.Second):
		t.Fatal("Spend blocked on the ledger's disk write: the state lock is held across the I/O")
	}

	close(release)
	<-done
}

func sessionName(i int) string {
	return "telegram:" + string(rune('a'+i%26)) + "-" + time.Duration(i).String()
}
