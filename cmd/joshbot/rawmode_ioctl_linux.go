//go:build linux

package main

import "golang.org/x/sys/unix"

// Linux reads and writes the termios structure with TCGETS/TCSETS.
const (
	ioctlReadTermios  = unix.TCGETS
	ioctlWriteTermios = unix.TCSETS
)
