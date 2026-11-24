//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package main

import "golang.org/x/sys/unix"

// The BSDs (macOS included) read and write the termios structure with
// TIOCGETA/TIOCSETA rather than Linux's TCGETS/TCSETS.
const (
	ioctlReadTermios  = unix.TIOCGETA
	ioctlWriteTermios = unix.TIOCSETA
)
