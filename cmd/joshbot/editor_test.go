package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// chanKeyReader feeds scripted key events to the editor. Push before or
// between ReadLine calls; the channel stays open so the reader goroutine may
// block forever on exhaustion, which tests rely on.
type chanKeyReader struct {
	ch chan keyEvent
}

func newChanKeyReader() *chanKeyReader {
	return &chanKeyReader{ch: make(chan keyEvent, 64)}
}

func (r *chanKeyReader) ReadKey() (keyEvent, error) {
	ev, ok := <-r.ch
	if !ok {
		return keyEvent{}, io.EOF
	}
	return ev, nil
}

func (r *chanKeyReader) push(ev keyEvent) { r.ch <- ev }

// pushStr inserts each rune as a printable key event.
func (r *chanKeyReader) pushStr(s string) {
	for _, c := range s {
		r.ch <- keyEvent{r: c}
	}
}

func newTestEditor(out *bytes.Buffer, r keyReader, commands []string) *lineEditor {
	e := newLineEditor(out, nil, commands)
	e.reader = r
	e.width = 80
	e.height = 24
	return e
}

func TestEditor_ReadLineSubmitsLine(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	r.pushStr("hello world")
	r.push(keyEvent{code: keyEnter})

	e := newTestEditor(&out, r, nil)
	line, err := e.ReadLine(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "hello world" {
		t.Fatalf("line = %q, want %q", line, "hello world")
	}
}

func TestEditor_BackspaceAndArrowMovement(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	r.pushStr("abc")
	r.push(keyEvent{code: keyLeft}) // cursor between b and c
	r.push(keyEvent{code: keyLeft}) // cursor after a
	r.push(keyEvent{code: keyBackspace})
	r.push(keyEvent{code: keyEnd})
	r.pushStr("!")
	r.push(keyEvent{code: keyEnter})

	e := newTestEditor(&out, r, nil)
	line, err := e.ReadLine(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "bc!" {
		t.Fatalf("line = %q, want %q", line, "bc!")
	}
}

func TestEditor_DeleteKeyRemovesForward(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	r.pushStr("abc")
	r.push(keyEvent{code: keyHome})
	r.push(keyEvent{code: keyDelete}) // remove 'a'
	r.push(keyEvent{code: keyEnter})

	e := newTestEditor(&out, r, nil)
	line, err := e.ReadLine(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "bc" {
		t.Fatalf("line = %q, want %q", line, "bc")
	}
}

func TestEditor_HomeEndMovement(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	r.pushStr("ab")
	r.push(keyEvent{code: keyHome})
	r.pushStr("X")
	r.push(keyEvent{code: keyEnd})
	r.pushStr("Y")
	r.push(keyEvent{code: keyEnter})

	e := newTestEditor(&out, r, nil)
	line, err := e.ReadLine(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "XabY" {
		t.Fatalf("line = %q, want %q", line, "XabY")
	}
}

func TestEditor_HistoryNavigation(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	e := newTestEditor(&out, r, nil)

	// Submit two lines.
	for _, want := range []string{"first line", "second line"} {
		r.pushStr(want)
		r.push(keyEvent{code: keyEnter})
		got, err := e.ReadLine(context.Background())
		if err != nil || got != want {
			t.Fatalf("ReadLine = %q, %v; want %q", got, err, want)
		}
	}

	// Up recalls most recent, then older; Down returns to a fresh line.
	r.push(keyEvent{code: keyUp})
	r.push(keyEvent{code: keyEnter})
	got, err := e.ReadLine(context.Background())
	if err != nil || got != "second line" {
		t.Fatalf("historyUp (most recent) = %q, %v", got, err)
	}

	r.push(keyEvent{code: keyUp})
	r.push(keyEvent{code: keyUp})
	r.push(keyEvent{code: keyEnter})
	got, err = e.ReadLine(context.Background())
	if err != nil || got != "first line" {
		t.Fatalf("historyUp (oldest) = %q, %v", got, err)
	}

	r.push(keyEvent{code: keyDown})
	r.push(keyEvent{code: keyDown})
	r.pushStr("third line")
	r.push(keyEvent{code: keyEnter})
	got, err = e.ReadLine(context.Background())
	if err != nil || got != "third line" {
		t.Fatalf("historyDown to fresh line = %q, %v", got, err)
	}
}

func TestEditor_TabCompletionCycles(t *testing.T) {
	var out bytes.Buffer
	commands := []string{"help", "history", "hike", "status"}
	e := newTestEditor(&out, nil, commands)

	for _, s := range "/h" {
		e.handleKey(keyEvent{r: s})
	}
	e.handleKey(keyEvent{code: keyTab}) // -> /help
	if got := e.Current(); got != "/help" {
		t.Fatalf("after 1st Tab buffer = %q, want %q", got, "/help")
	}
	e.handleKey(keyEvent{code: keyTab}) // -> /history
	if got := e.Current(); got != "/history" {
		t.Fatalf("after 2nd Tab buffer = %q, want %q", got, "/history")
	}
	e.handleKey(keyEvent{code: keyTab}) // -> /hike
	if got := e.Current(); got != "/hike" {
		t.Fatalf("after 3rd Tab buffer = %q, want %q", got, "/hike")
	}
	e.handleKey(keyEvent{code: keyTab}) // wraps -> /help
	if got := e.Current(); got != "/help" {
		t.Fatalf("after 4th Tab buffer = %q, want %q", got, "/help")
	}

	// A hint line listing the candidates must be rendered on the next redraw.
	e.render()
	if !strings.Contains(out.String(), "complete:") {
		t.Fatalf("output missing completion hint:\n%s", out.String())
	}

	if done, line, _ := e.handleKey(keyEvent{code: keyEnter}); !done || line != "/help" {
		t.Fatalf("handleKey(enter) = done %v line %q, want done=true line %q", done, line, "/help")
	}
}

func TestEditor_TabNoMatchLeavesBuffer(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, nil, []string{"help", "status"})

	for _, s := range "/zzz" {
		e.handleKey(keyEvent{r: s})
	}
	e.handleKey(keyEvent{code: keyTab})
	if got := e.Current(); got != "/zzz" {
		t.Fatalf("buffer = %q, want %q", got, "/zzz")
	}
}

func TestEditor_MultilineAltEnter(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	r.pushStr("line one")
	r.push(keyEvent{code: keyNewline})
	r.pushStr("line two")
	r.push(keyEvent{code: keyEnter})

	e := newTestEditor(&out, r, nil)
	line, err := e.ReadLine(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "line one\nline two" {
		t.Fatalf("line = %q, want %q", line, "line one\nline two")
	}
}

func TestEditor_CtrlCQuits(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	r.push(keyEvent{code: keyCtrlC})

	e := newTestEditor(&out, r, nil)
	if _, err := e.ReadLine(context.Background()); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestEditor_CtrlDQuitsOnEmptyBuffer(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	r.push(keyEvent{code: keyCtrlD})

	e := newTestEditor(&out, r, nil)
	if _, err := e.ReadLine(context.Background()); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestEditor_ContextCancellation(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	e := newTestEditor(&out, r, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.ReadLine(ctx); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// close() must be safe to call more than once, and ReadLine must return on
// context cancellation even when the reader is blocked and never delivers a
// key. That is the shutdown path: runAgentLoop cancels ctx, ReadLine returns
// ctx.Err(), and the deferred editor.close() stops the reader goroutine. The
// goroutine itself may stay parked inside a blocking ReadKey until a byte
// arrives or the descriptor closes — the same accepted contract as the
// buffered reader goroutine in runAgentLoop — so no test asserts it returns
// the instant close() is called.
func TestEditor_ReadLineReturnsOnContextCancelWhileBlocked(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader() // never delivers a key
	e := newTestEditor(&out, r, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.ReadLine(ctx)
		close(done)
	}()

	// Give the reader goroutine time to block on ReadKey, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLine did not return after ctx cancel; shutdown path is broken")
	}
}

func TestEditor_CloseIsIdempotent(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, newChanKeyReader(), nil)
	e.close()
	e.close() // must not panic
}

func TestWrapRunes_WideRuneNotSplit(t *testing.T) {
	got := wrapRunes([]rune("ab中cd"), 3, 5)
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2: %q", len(got), got)
	}
	if string(got[0]) != "ab" || string(got[1]) != "中cd" {
		t.Fatalf("chunks = %q, %q; want %q, %q", got[0], got[1], "ab", "中cd")
	}
}

func TestBuildView_SetsCursorAndRows(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, newChanKeyReader(), nil)
	e.prompt = "> "
	e.setBuffer("abc")
	e.cursor = 3

	view := e.buildView()
	if len(view.rows) != 1 || view.rows[0] != "> abc" {
		t.Fatalf("rows = %q, want [\"> abc\"]", view.rows)
	}
	if view.cursorRow != 0 || view.cursorCol != 5 {
		t.Fatalf("cursor = (%d,%d), want (0,5)", view.cursorRow, view.cursorCol)
	}
}

// A wide rune (CJK, display width 2) before the cursor must shift the cursor
// column by its full width, not by the rune count. This is the case the go
// systems review flagged: cursorCol used the rune offset, so the cursor was
// drawn one cell left whenever the first line contained a wide rune.
func TestBuildView_WideRuneShiftsCursorColumn(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, newChanKeyReader(), nil)
	e.prompt = "> "
	e.setBuffer("中abc") // display width: 中=2, a=b=c=1
	e.cursor = len([]rune("中abc"))

	view := e.buildView()
	if len(view.rows) != 1 {
		t.Fatalf("rows = %v, want a single row", view.rows)
	}
	// prompt (2) + 中 (2) + abc (3) = column 7
	if view.cursorCol != 7 {
		t.Fatalf("cursorCol = %d, want 7 (prompt 2 + wide rune 2 + 3)", view.cursorCol)
	}
}

func TestBuildEditorPrompt(t *testing.T) {
	withModel := buildEditorPrompt("smart")
	if !strings.Contains(withModel, "smart") || !strings.Contains(withModel, "❯") {
		t.Fatalf("prompt with model = %q", withModel)
	}
	without := buildEditorPrompt("")
	if strings.Contains(without, "smart") {
		t.Fatalf("prompt without model unexpectedly mentions a model: %q", without)
	}
}

// TestEditor_RenderCursorUpIsSingleRow verifies the render() cursor math:
// after printing rows each followed by \r\n the real cursor sits one line
// below the view, so a single-row view must move up exactly one line back to
// the prompt row. An off-by-one here leaves the cursor below the view and the
// next hide() fails to clear the prompt line, so every keystroke reprints the
// `● model ❯` line above the new one.
func TestEditor_RenderCursorUpIsSingleRow(t *testing.T) {
	var out bytes.Buffer
	r := newChanKeyReader()
	e := newTestEditor(&out, r, nil)
	e.prompt = "❯ "
	e.buf = []rune("hi")
	e.cursor = 2

	e.render()
	got := out.String()
	// The cursor-up sequence must move exactly one line back up to the prompt
	// row (rows=1, cursorRow=0 -> up=1). Missing it is the off-by-one bug.
	if !strings.Contains(got, "\x1b[1A") {
		t.Fatalf("render output missing cursor-up-to-prompt escape:\n%q", got)
	}
}
