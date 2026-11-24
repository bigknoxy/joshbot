//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

// The interactive TUI used to drop the carriage return: raw mode cleared
// OPOST, so a written "\n" moved the cursor down without returning it, and
// every line of a multi-line reply started where the previous one ended
// (#503). The input must be raw; the output must still translate newlines.
func TestApplyRawFlagsKeepsNewlineTranslation(t *testing.T) {
	var termios unix.Termios
	applyRawFlags(&termios)

	if termios.Oflag&unix.OPOST == 0 {
		t.Error("OPOST is cleared: the kernel would stop translating \\n into \\r\\n")
	}
	if termios.Oflag&unix.ONLCR == 0 {
		t.Error("ONLCR is cleared: a written \\n would not return the carriage")
	}
	if termios.Lflag&unix.ICANON != 0 {
		t.Error("ICANON is still set: input is not raw")
	}
	if termios.Lflag&unix.ECHO != 0 {
		t.Error("ECHO is still set: the editor's keystrokes would be echoed twice")
	}
	if termios.Cc[unix.VMIN] != 1 || termios.Cc[unix.VTIME] != 0 {
		t.Errorf("VMIN/VTIME = %d/%d, want 1/0", termios.Cc[unix.VMIN], termios.Cc[unix.VTIME])
	}
}
