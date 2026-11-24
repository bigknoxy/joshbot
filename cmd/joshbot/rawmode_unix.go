//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import "golang.org/x/sys/unix"

// rawTermios reads the terminal's current settings and returns the raw-mode
// version of them.
func rawTermios(fd int) (*unix.Termios, error) {
	t, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return nil, err
	}
	applyRawFlags(t)
	return t, nil
}

// applyRawFlags turns the terminal's input into raw mode — the editor needs
// every keystroke as it arrives, with no echo, no signal keys and no
// canonical line buffering.
//
// Output is deliberately NOT made raw. cfmakeraw (and therefore
// term.MakeRaw) clears OPOST, and OPOST is what makes the kernel translate a
// written "\n" into "\r\n". With it off, a bare "\n" moves the cursor down
// without returning the carriage, so every line of a multi-line reply starts
// at the column the previous line ended — the staircase a user sees in the
// interactive TUI (#503). Keeping post-processing on, and forcing ONLCR so
// the translation is on regardless of the user's settings, gives every
// writer — the agent's reply, the logger on stderr, the spinner, anything
// added later — the cooked-mode behaviour it expects.
//
// The editor's own writes use an explicit "\r\n", which becomes "\r\r\n"
// here; a carriage return at column 0 is a no-op, so nothing changes.
func applyRawFlags(t *unix.Termios) {
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag |= unix.OPOST | unix.ONLCR
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
}

// setRawMode puts the terminal into raw mode and leaves it there; the
// caller restores the saved state.
func setRawMode(fd int) error {
	t, err := rawTermios(fd)
	if err != nil {
		return err
	}
	return unix.IoctlSetTermios(fd, ioctlWriteTermios, t)
}
