package main

import (
	"bytes"
	"strings"
	"testing"
)

// buildView decides what the interactive CLI actually draws, and every branch in
// it fails the same way: the text is right but the cursor is somewhere else, so
// typing lands in the wrong place with nothing on screen to explain it. The
// happy path (one short line) is covered; the branches that only fire on a real
// terminal are not — a completion hint above the prompt, a buffer taller than
// the window, an unset width or height, and a prompt wide enough to leave no
// room for text.

// The hint row is prepended above the prompt, so everything below it moves down
// one. Forgetting to move the cursor with it draws the caret on the hint line
// while the characters appear on the line beneath.
func TestBuildViewCompletionHintPushesTheCursorDown(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, newChanKeyReader(), nil)
	e.prompt = "> "
	e.setBuffer("/he")
	e.cursor = 3
	e.compMatches = []string{"help", "heartbeat"}
	e.compIdx = 0

	view := e.buildView()
	if len(view.rows) != 2 {
		t.Fatalf("rows = %q, want a hint row above the input row", view.rows)
	}
	if !strings.Contains(view.rows[0], "complete:") {
		t.Errorf("row 0 = %q, want the completion hint", view.rows[0])
	}
	if view.cursorRow != 1 {
		t.Errorf("cursorRow = %d, want 1: the hint row shifts the input down", view.cursorRow)
	}
}

// A buffer taller than the window scrolls: the earliest rows are dropped so the
// cursor stays on screen. Without this the caret is drawn below the last visible
// row and the user types into a line they cannot see.
func TestBuildViewScrollsToKeepTheCursorOnScreen(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, newChanKeyReader(), nil)
	e.prompt = ""
	e.width = 20
	e.height = 5 // 4 usable rows
	buf := "l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9"
	e.setBuffer(buf)
	e.cursor = len(buf) // end of the last line

	view := e.buildView()
	if len(view.rows) != 4 {
		t.Fatalf("rows = %d, want 4 (height 5 minus one)", len(view.rows))
	}
	if view.cursorRow != 3 {
		t.Fatalf("cursorRow = %d, want 3 (the last visible row)", view.cursorRow)
	}
	if view.rows[view.cursorRow] != "l9" {
		t.Errorf("cursor row holds %q, want the line the cursor is on (l9)", view.rows[view.cursorRow])
	}
	if view.rows[0] != "l6" {
		t.Errorf("first visible row = %q, want l6 (the earliest rows dropped)", view.rows[0])
	}
}

// The scroll is clamped at the cursor row: with the cursor on the first line, an
// oversized buffer must not be scrolled at all. Dropping past the cursor puts it
// at a negative row, which indexes off the front of the rows slice.
func TestBuildViewNeverScrollsPastTheCursorRow(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, newChanKeyReader(), nil)
	e.prompt = ""
	e.width = 20
	e.height = 5
	e.setBuffer("l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9")
	e.cursor = 0 // first line, first column

	view := e.buildView()
	if view.cursorRow != 0 {
		t.Fatalf("cursorRow = %d, want 0; scrolling past the cursor loses it", view.cursorRow)
	}
	if len(view.rows) == 0 || view.rows[view.cursorRow] != "l0" {
		t.Fatalf("rows = %q, want the cursor's own line still present at row 0", view.rows)
	}
}

// width and height are zero until the first terminal size query answers, and on
// a pipe or a terminal that never answers they stay zero. Wrapping at column 0
// would produce a row per character; the fallback has to be a usable 80x24.
func TestBuildViewFallsBackToEightyByTwentyFour(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, newChanKeyReader(), nil)
	e.prompt = "> "
	e.width = 0
	e.height = 0
	e.setBuffer(strings.Repeat("a", 100))
	e.cursor = 100

	view := e.buildView()
	// 80 columns minus the 2-column prompt leaves 78 on the first row.
	if len(view.rows) != 2 {
		t.Fatalf("rows = %d, want 2 (78 on the prompt row, 22 on the next)", len(view.rows))
	}
	if got := len(view.rows[0]); got != 80 {
		t.Errorf("row 0 is %d columns, want 80 (prompt 2 + 78 of text)", got)
	}
	if view.cursorRow != 1 || view.cursorCol != 22 {
		t.Errorf("cursor = (%d,%d), want (1,22)", view.cursorRow, view.cursorCol)
	}
}

// The height fallback is separate from the width one, and only a buffer taller
// than the assumed window shows it: too small a default hides earlier lines of
// the answer the user is looking at, too large scrolls them off the top of the
// real terminal.
func TestBuildViewFallsBackToTwentyFourRows(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, newChanKeyReader(), nil)
	e.prompt = ""
	e.width = 20
	e.height = 0

	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "l"
	}
	buf := strings.Join(lines, "\n")
	e.setBuffer(buf)
	e.cursor = len(buf)

	view := e.buildView()
	if len(view.rows) != 23 {
		t.Fatalf("rows = %d, want 23 (an assumed height of 24 minus one)", len(view.rows))
	}
}

// A prompt as wide as the terminal leaves no room for text. The budget is
// floored rather than allowed to go zero or negative: wrapRunes with a
// non-positive budget makes no progress, so the row count is unbounded.
func TestBuildViewFloorsTheFirstRowBudget(t *testing.T) {
	var out bytes.Buffer
	e := newTestEditor(&out, newChanKeyReader(), nil)
	e.width = 20
	e.prompt = strings.Repeat("p", 20) // the whole width
	e.setBuffer("abcdefgh")
	e.cursor = 8

	view := e.buildView()
	if len(view.rows) != 2 {
		t.Fatalf("rows = %q, want 2 (4 floored onto the prompt row, the rest below)", view.rows)
	}
	if view.rows[0] != e.prompt+"abcd" {
		t.Errorf("row 0 = %q, want the prompt plus the floored 4 columns", view.rows[0])
	}
	if view.rows[1] != "efgh" {
		t.Errorf("row 1 = %q, want the remainder", view.rows[1])
	}
}
