//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package main

import "golang.org/x/term"

// setRawMode falls back to term.MakeRaw on platforms whose termios ioctl
// constants are not covered here (Windows, Solaris, AIX). The input is raw;
// the newline translation is whatever the platform's raw mode does, which is
// the behaviour this project had before #503.
func setRawMode(fd int) error {
	_, err := term.MakeRaw(fd)
	return err
}
