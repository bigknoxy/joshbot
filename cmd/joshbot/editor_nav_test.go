package main

import (
	"testing"
)

// editorWith returns an editor holding s with the cursor at rune index cursor.
func editorWith(s string, cursor int) *lineEditor {
	e := &lineEditor{buf: []rune(s)}
	e.cursor = cursor
	return e
}

// Up/Down in a multiline buffer are pure index arithmetic over rune offsets,
// and every one of these cases is an off-by-one that would put the cursor on
// the wrong line — the class of bug that makes an editor eat a character from
// the line above. The column must also be *clamped* to a shorter neighbouring
// line rather than running past its newline, which would move the cursor onto
// a line the user did not point at.
func TestEditorVerticalMovementClampsTheColumn(t *testing.T) {
	// "abcdef\nxy\nlonger line"
	//  0......6  7..9 10.......
	const buf = "abcdef\nxy\nlonger line"

	tests := []struct {
		name   string
		cursor int
		move   func(*lineEditor)
		want   int
	}{
		{"up from a long line onto a short one clamps to its end", 16, (*lineEditor).moveUp, 9},
		{"up from the middle keeps the column", 13, (*lineEditor).moveUp, 9},
		{"up from line 1 lands on line 0 at the same column", 8, (*lineEditor).moveUp, 1},
		{"up on line 0 is a no-op, not a move to -1", 3, (*lineEditor).moveUp, 3},
		{"down from a long line clamps to the shorter one", 4, (*lineEditor).moveDown, 9},
		{"down from the last line is a no-op", 14, (*lineEditor).moveDown, 14},
		{"down keeps the column when the next line is long enough", 8, (*lineEditor).moveDown, 11},
		{"down from the very end stays put", len(buf), (*lineEditor).moveDown, len(buf)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := editorWith(buf, tt.cursor)
			tt.move(e)
			if e.cursor != tt.want {
				t.Errorf("cursor = %d (line %d), want %d (line %d)",
					e.cursor, e.lineOf(e.cursor), tt.want, e.lineOf(tt.want))
			}
		})
	}
}

// lineOf and lineStart are the coordinate system moveUp/moveDown are built on;
// if they disagree — lineStart(lineOf(c)) not being at or before c — vertical
// movement drifts a line per keypress. Both boundaries matter: a cursor sitting
// exactly on a newline belongs to the line that newline terminates.
func TestEditorLineCoordinatesAgree(t *testing.T) {
	const buf = "abcdef\nxy\nlonger line"
	e := editorWith(buf, 0)

	for c := 0; c <= len([]rune(buf)); c++ {
		line := e.lineOf(c)
		start := e.lineStart(line)
		if start > c {
			t.Fatalf("cursor %d is on line %d, but that line starts at %d", c, line, start)
		}
		if got := e.lineOf(start); got != line {
			t.Fatalf("lineStart(%d) = %d, which lineOf reports as line %d", line, start, got)
		}
	}
	// Past the last line, lineStart must saturate at the buffer end rather than
	// return an index that would panic when sliced.
	if got := e.lineStart(99); got != len([]rune(buf)) {
		t.Errorf("lineStart past the end = %d, want %d", got, len([]rune(buf)))
	}
	if got := e.lineStart(-1); got != 0 {
		t.Errorf("lineStart(-1) = %d, want 0", got)
	}
}

// moveRight must stop at the end of the buffer; running past it puts the
// cursor at an index every slice in render and lineBounds would panic on.
func TestEditorMoveRightStopsAtTheEnd(t *testing.T) {
	e := editorWith("ab", 1)
	e.moveRight()
	if e.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", e.cursor)
	}
	e.moveRight()
	if e.cursor != 2 {
		t.Errorf("moveRight ran past the end of the buffer: cursor = %d, len = %d", e.cursor, len(e.buf))
	}
}

// Up/Down on a *single-line* buffer are history recall, not cursor movement,
// and walking back down past the newest entry must restore an empty line —
// returning the newest entry again strands the user unable to type a fresh
// command without clearing the line by hand.
func TestEditorHistoryWalkEndsAtAnEmptyLine(t *testing.T) {
	e := &lineEditor{history: []string{"first", "second"}}
	e.histIdx = len(e.history)

	e.moveUp()
	if string(e.buf) != "second" {
		t.Fatalf("first Up = %q, want the newest entry", string(e.buf))
	}
	e.moveUp()
	if string(e.buf) != "first" {
		t.Fatalf("second Up = %q, want the older entry", string(e.buf))
	}
	// Past the oldest entry, Up must stop rather than index out of range.
	e.moveUp()
	if string(e.buf) != "first" {
		t.Errorf("Up past the oldest entry = %q, want it to stay", string(e.buf))
	}
	// The cursor follows the recalled text, or the next keystroke inserts in
	// the middle of it.
	if e.cursor != len(e.buf) {
		t.Errorf("cursor = %d after recall, want the end (%d)", e.cursor, len(e.buf))
	}

	e.moveDown()
	if string(e.buf) != "second" {
		t.Fatalf("Down = %q, want the newer entry", string(e.buf))
	}
	e.moveDown()
	if string(e.buf) != "" {
		t.Errorf("Down past the newest entry = %q, want an empty line", string(e.buf))
	}
	e.moveDown()
	if string(e.buf) != "" {
		t.Errorf("Down past the end = %q, want it to stay empty", string(e.buf))
	}
}
