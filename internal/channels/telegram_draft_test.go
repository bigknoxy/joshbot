package channels

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type rawCall struct {
	method  string
	chatID  int64
	draftID int64
	text    string
}

type fakeRawCaller struct {
	mu    sync.Mutex
	calls []rawCall
	errs  []error
}

func (f *fakeRawCaller) Raw(method string, payload interface{}) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := rawCall{method: method}
	if m, ok := payload.(map[string]interface{}); ok {
		c.chatID, _ = m["chat_id"].(int64)
		c.draftID, _ = m["draft_id"].(int64)
		c.text, _ = m["text"].(string)
	}
	f.calls = append(f.calls, c)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	return []byte(`{"ok":true,"result":true}`), nil
}

func (f *fakeRawCaller) snapshot() []rawCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rawCall(nil), f.calls...)
}

// draftTestChannel wires a channel with drafts enabled, a fake editor and a
// fake raw caller.
func draftTestChannel(t *testing.T, interval time.Duration) (*TelegramChannel, *fakeEditor, *fakeRawCaller) {
	t.Helper()
	tg, ed := streamTestChannel(t, interval)
	raw := &fakeRawCaller{}
	tg.mu.Lock()
	tg.rawCaller = raw
	tg.cfg.StreamDrafts = true
	tg.mu.Unlock()
	return tg, ed, raw
}

func TestDraftsCarryDeltasAndFinishStillPersistsTheReply(t *testing.T) {
	tg, ed, raw := draftTestChannel(t, time.Nanosecond)
	var slept time.Duration
	s := newTestStreamer(t, tg, 4242, &slept)

	s.Delta("hel")
	s.Delta("lo")

	calls := raw.snapshot()
	if len(calls) != 2 {
		t.Fatalf("raw calls = %d, want 2: %+v", len(calls), calls)
	}
	for _, c := range calls {
		if c.method != sendMessageDraftMethod {
			t.Errorf("method = %q, want %q", c.method, sendMessageDraftMethod)
		}
		if c.chatID != 4242 {
			t.Errorf("chat_id = %d, want 4242", c.chatID)
		}
		if c.draftID == 0 {
			t.Error("draft_id must be non-zero")
		}
	}
	if calls[0].draftID != calls[1].draftID {
		t.Error("one turn must reuse one draft id so the client animates it")
	}
	if got := calls[1].text; got != "hello" {
		t.Errorf("second draft text = %q, want %q", got, "hello")
	}

	// A draft is ephemeral, so nothing has reached the chat yet: no edit-loop
	// traffic, and Delivered must stay false.
	if n := len(ed.snapshot()); n != 0 {
		t.Fatalf("editor calls during draft streaming = %d, want 0: %+v", n, ed.snapshot())
	}
	if s.Delivered() {
		t.Fatal("a draft write must never set delivered - Finish's suppression contract depends on it")
	}

	if !s.Finish(nil) {
		t.Fatal("Finish must report the whole answer delivered after the persisting send")
	}
	if got := ed.lastFor("4242"); got != "hello" {
		t.Errorf("persisted message = %q, want %q", got, "hello")
	}
}

func TestARefusedDraftFallsBackToTheEditLoopWithoutBreakingTheTurn(t *testing.T) {
	tg, ed, raw := draftTestChannel(t, time.Nanosecond)
	raw.mu.Lock()
	raw.errs = []error{errors.New("Bad Request: method not found")}
	raw.mu.Unlock()
	var slept time.Duration
	s := newTestStreamer(t, tg, 4242, &slept)

	s.Delta("hello")

	if got := ed.lastFor("4242"); got != "hello" {
		t.Fatalf("edit-loop text after a refused draft = %q, want %q", got, "hello")
	}
	if !s.Delivered() {
		t.Fatal("the fallback send must deliver")
	}

	s.Delta(" again")
	if n := len(raw.snapshot()); n != 1 {
		t.Errorf("raw calls = %d, want 1 - drafts must stay off for the turn", n)
	}
	if got := ed.lastFor("4242"); got != "hello again" {
		t.Errorf("text = %q, want %q", got, "hello again")
	}
	if !s.Finish(nil) {
		t.Error("a refused draft must not break the turn")
	}
}

func TestThinkingShowsAnEmptyDraftPlaceholder(t *testing.T) {
	tg, ed, raw := draftTestChannel(t, time.Nanosecond)
	var slept time.Duration
	s := newTestStreamer(t, tg, 4242, &slept)

	s.Thinking()

	calls := raw.snapshot()
	if len(calls) != 1 {
		t.Fatalf("raw calls = %d, want 1", len(calls))
	}
	if calls[0].text != "" {
		t.Errorf("placeholder text = %q, want empty (the native Thinking placeholder)", calls[0].text)
	}
	if calls[0].draftID == 0 {
		t.Error("draft_id must be non-zero")
	}
	if n := len(ed.snapshot()); n != 0 {
		t.Errorf("editor calls = %d, want 0", n)
	}

	// Once text exists the placeholder is meaningless and must not re-fire.
	s.Delta("hi")
	s.Thinking()
	if n := len(raw.snapshot()); n != 2 {
		t.Errorf("raw calls after Delta+Thinking = %d, want 2", n)
	}
}

func TestDraftsAreOffForGroupsAndWhenNotConfigured(t *testing.T) {
	t.Run("group chat", func(t *testing.T) {
		tg, ed, raw := draftTestChannel(t, time.Nanosecond)
		var slept time.Duration
		// Telegram group ids are negative; sendMessageDraft takes a private
		// chat only, so the group must never spend a request per delta.
		s := newTestStreamer(t, tg, -100200300, &slept)
		s.Delta("hello")
		if n := len(raw.snapshot()); n != 0 {
			t.Errorf("raw calls for a group chat = %d, want 0", n)
		}
		if got := ed.lastFor("-100200300"); got != "hello" {
			t.Errorf("edit-loop text = %q, want %q", got, "hello")
		}
	})

	t.Run("stream_drafts off", func(t *testing.T) {
		tg, ed, raw := draftTestChannel(t, time.Nanosecond)
		tg.mu.Lock()
		tg.cfg.StreamDrafts = false
		tg.mu.Unlock()
		var slept time.Duration
		s := newTestStreamer(t, tg, 4242, &slept)
		s.Delta("hello")
		if n := len(raw.snapshot()); n != 0 {
			t.Errorf("raw calls with drafts disabled = %d, want 0", n)
		}
		if got := ed.lastFor("4242"); got != "hello" {
			t.Errorf("edit-loop text = %q, want %q", got, "hello")
		}
	})
}

func TestStatusRidesTheDraftAndLeavesNoMessageToClean(t *testing.T) {
	tg, ed, raw := draftTestChannel(t, time.Nanosecond)
	var slept time.Duration
	s := newTestStreamer(t, tg, 4242, &slept)

	s.Status("⚙️ shell: go test ./...")
	calls := raw.snapshot()
	if len(calls) != 1 || calls[0].text != "⚙️ shell: go test ./..." {
		t.Fatalf("status draft calls = %+v", calls)
	}
	if n := len(ed.snapshot()); n != 0 {
		t.Fatalf("a status line must not create a message in draft mode: %+v", ed.snapshot())
	}

	// A reply that never arrives leaves nothing to delete: the draft expires
	// on its own, so Finish must not try to clean up a message it never sent.
	if s.Finish(nil) {
		t.Error("Finish must report undelivered when no reply text arrived")
	}
	if n := len(ed.snapshot()); n != 0 {
		t.Errorf("editor calls after a status-only turn = %d, want 0: %+v", n, ed.snapshot())
	}
}

func TestDraftTailTrimsOnRuneBoundaries(t *testing.T) {
	short := "hello"
	if got := draftTail(short); got != short {
		t.Errorf("draftTail(short) = %q, want unchanged", got)
	}

	// Each 世 is three bytes, so the byte-aligned cut at 4096 lands two bytes
	// into a rune - the case a naive slice gets wrong.
	// One ASCII byte ahead of the runes makes the byte-aligned cut land in the
	// middle of a two-byte rune, which is the case that matters.
	long := strings.Repeat("世", TelegramMaxMessageLen)
	got := draftTail(long)
	if len(got) > TelegramMaxMessageLen {
		t.Errorf("len = %d, want <= %d", len(got), TelegramMaxMessageLen)
	}
	if !strings.HasSuffix(long, got) {
		t.Error("draftTail must keep the tail of the text")
	}
	for i, r := range got {
		if r == '�' {
			t.Fatalf("invalid UTF-8 at byte %d - Telegram rejects the whole request for it", i)
		}
	}
}

func TestDraftIDsAreNonZeroAndDistinctPerTurn(t *testing.T) {
	tg, _, raw := draftTestChannel(t, time.Nanosecond)
	var slept time.Duration
	seen := map[int64]bool{}
	for i := 0; i < 3; i++ {
		s := newTestStreamer(t, tg, 4242, &slept)
		s.Delta(fmt.Sprintf("turn %d", i))
		calls := raw.snapshot()
		id := calls[len(calls)-1].draftID
		if id == 0 {
			t.Fatal("draft_id must be non-zero")
		}
		if seen[id] {
			t.Fatalf("draft_id %d reused across turns - one turn's text would animate into another's", id)
		}
		seen[id] = true
	}
}
