package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// readerOnFD hands the reader its *own* descriptor. newOSKeyReader calls
// os.NewFile, which takes ownership and gets a finalizer that closes the fd — so
// passing pr.Fd() directly would give one descriptor two owners and close it
// twice. The second close lands on whatever fd number has since been reused,
// which on CI was the coverage writer: every test passed and the package still
// failed with "error generating coverage report: bad file descriptor".
func readerOnFD(t *testing.T, fd int) *osKeyReader {
	t.Helper()

	dup, err := syscall.Dup(fd)
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	return newOSKeyReader(dup)
}

// osKeyReader turns raw terminal bytes into key events for the interactive
// editor. It is the only thing standing between a terminal and the line editor,
// and every mapping here is invisible until a user presses the key: an arrow
// that inserts "[A" or a Delete that does nothing is a bug nobody sees in CI.
//
// The reader takes a file descriptor, so the tests drive it through an os.Pipe.

// keyReaderOn returns a reader fed by the bytes in input.
func keyReaderOn(t *testing.T, input string) *osKeyReader {
	t.Helper()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := pw.WriteString(input); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() {
		_ = pw.Close()
		_ = pr.Close()
	})
	return readerOnFD(t, int(pr.Fd()))
}

func TestDecodeByteMapsControlCodes(t *testing.T) {
	cases := []struct {
		name string
		b    byte
		want keyEvent
	}{
		{"carriage return is Enter", '\r', keyEvent{code: keyEnter}},
		{"newline is Enter", '\n', keyEvent{code: keyEnter}},
		{"tab", '\t', keyEvent{code: keyTab}},
		{"DEL is backspace", 0x7f, keyEvent{code: keyBackspace}},
		{"BS is backspace", 0x08, keyEvent{code: keyBackspace}},
		{"Ctrl+C", 0x03, keyEvent{code: keyCtrlC}},
		{"Ctrl+D", 0x04, keyEvent{code: keyCtrlD}},
		{"Ctrl+A is Home", 0x01, keyEvent{code: keyHome}},
		{"Ctrl+E is End", 0x05, keyEvent{code: keyEnd}},
		{"printable byte is a rune", 'x', keyEvent{r: 'x'}},
		// An unmapped control byte must produce nothing rather than a stray
		// rune the editor would insert into the user's line.
		{"unmapped control byte is ignored", 0x00, keyEvent{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeByte(tc.b); got != tc.want {
				t.Errorf("decodeByte(%#x) = %+v, want %+v", tc.b, got, tc.want)
			}
		})
	}
}

// The CSI sequences the editor actually acts on. If one of these stopped being
// recognised the bytes would fall through as literal text.
func TestReadKeyDecodesCSISequences(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  keyEvent
	}{
		{"up", "\x1b[A", keyEvent{code: keyUp}},
		{"down", "\x1b[B", keyEvent{code: keyDown}},
		{"right", "\x1b[C", keyEvent{code: keyRight}},
		{"left", "\x1b[D", keyEvent{code: keyLeft}},
		{"home", "\x1b[H", keyEvent{code: keyHome}},
		{"end", "\x1b[F", keyEvent{code: keyEnd}},
		{"shift-tab is treated as tab", "\x1b[Z", keyEvent{code: keyTab}},
		{"delete", "\x1b[3~", keyEvent{code: keyDelete}},
		{"home (vt sequence)", "\x1b[1~", keyEvent{code: keyHome}},
		{"end (vt sequence)", "\x1b[4~", keyEvent{code: keyEnd}},
		// SS3, which is how some terminals send Home/End.
		{"SS3 home", "\x1bOH", keyEvent{code: keyHome}},
		{"SS3 end", "\x1bOF", keyEvent{code: keyEnd}},
		// Alt+Enter is the only Alt combination the editor acts on: it inserts
		// a newline instead of submitting.
		{"alt+enter is a newline", "\x1b\r", keyEvent{code: keyNewline}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := keyReaderOn(t, tc.input)
			got, err := r.ReadKey()
			if err != nil {
				t.Fatalf("ReadKey: %v", err)
			}
			if got != tc.want {
				t.Errorf("ReadKey(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

// A modified arrow (ESC [ 1 ; 5 C) carries parameters before the final byte.
// The parameter bytes must be consumed, not left in the stream to be read back
// as literal text on the next key.
func TestReadKeyConsumesCSIParameters(t *testing.T) {
	r := keyReaderOn(t, "\x1b[1;5Cz")

	got, err := r.ReadKey()
	if err != nil {
		t.Fatalf("ReadKey: %v", err)
	}
	if got != (keyEvent{code: keyRight}) {
		t.Errorf("first key = %+v, want Right", got)
	}

	next, err := r.ReadKey()
	if err != nil {
		t.Fatalf("second ReadKey: %v", err)
	}
	if next != (keyEvent{r: 'z'}) {
		t.Errorf("second key = %+v, want the literal 'z'; parameter bytes leaked into the stream", next)
	}
}

// A bare ESC with nothing behind it is ignored rather than becoming a key, and
// a byte that arrives after the ESC timeout is buffered rather than dropped.
func TestReadKeyBareEscapeIsIgnoredAndNextByteSurvives(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = pw.Close()
		_ = pr.Close()
	})
	r := readerOnFD(t, int(pr.Fd()))

	if _, err := pw.WriteString("\x1b"); err != nil {
		t.Fatalf("write ESC: %v", err)
	}
	ev, err := r.ReadKey()
	if err != nil {
		t.Fatalf("ReadKey: %v", err)
	}
	if ev != (keyEvent{}) {
		t.Errorf("bare ESC produced %+v, want no key event", ev)
	}

	// The byte that arrives after the timeout must still be readable.
	if _, err := pw.WriteString("q"); err != nil {
		t.Fatalf("write q: %v", err)
	}
	// The timed-out peek left a goroutine blocked on the read; give it a moment
	// to land the byte in the pending buffer before asking for the next key.
	time.Sleep(200 * time.Millisecond)
	ev, err = r.ReadKey()
	if err != nil {
		t.Fatalf("ReadKey after ESC: %v", err)
	}
	if ev != (keyEvent{r: 'q'}) {
		t.Errorf("post-timeout key = %+v, want 'q'; the byte was dropped", ev)
	}
}

// terminalSize falls back to a usable 80x24 rather than 0x0 when the fd is not
// a terminal — a zero width makes the editor's wrapping arithmetic divide by
// nothing.
func TestTerminalSizeFallsBackWhenNotATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notatty")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	defer f.Close()

	w, h := terminalSize(int(f.Fd()))
	if w != 80 || h != 24 {
		t.Errorf("terminalSize on a non-terminal = %dx%d, want 80x24", w, h)
	}
}

// restoreTerminal with no saved state is a no-op, not a nil dereference: the
// interactive loop defers it even on paths where raw mode was never entered.
func TestRestoreTerminalWithNilStateIsSafe(t *testing.T) {
	restoreTerminal(0, nil)
}
