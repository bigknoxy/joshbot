package main

// Terminal key parsing and raw-mode management for the TUI line editor.
//
// osKeyReader turns raw terminal bytes into keyEvent values, decoding the
// common ANSI escape sequences (arrows, Home/End, Delete) and the Ctrl+letter
// codes. A bare ESC is distinguished from an escape sequence with a short
// timeout; a byte that arrives just after the timeout is buffered, never
// silently dropped.

import (
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// readTimeout is how long the parser waits after an ESC byte for the rest of
// an escape sequence before treating it as a bare ESC.
const readTimeout = 60 * time.Millisecond

// osKeyReader parses key events from a terminal file descriptor already in
// raw mode. Reads block until a key arrives.
type osKeyReader struct {
	fd      int
	file    *os.File
	mu      sync.Mutex
	pending []byte
}

func newOSKeyReader(fd int) *osKeyReader {
	return &osKeyReader{fd: fd, file: os.NewFile(uintptr(fd), "terminal")}
}

func (r *osKeyReader) ReadKey() (keyEvent, error) {
	b, err := r.readByte()
	if err != nil {
		return keyEvent{}, err
	}

	if b == 0x1b {
		return r.readEscape()
	}

	return decodeByte(b), nil
}

// readByte returns the next pending or freshly-read byte.
func (r *osKeyReader) readByte() (byte, error) {
	r.mu.Lock()
	if len(r.pending) > 0 {
		b := r.pending[0]
		r.pending = r.pending[1:]
		r.mu.Unlock()
		return b, nil
	}
	r.mu.Unlock()

	buf := make([]byte, 1)
	n, err := r.file.Read(buf)
	if n > 0 {
		return buf[0], nil
	}
	if err != nil {
		return 0, err
	}
	return 0, io.EOF
}

// peekByte returns the next byte without consuming it, waiting at most
// readTimeout. On timeout it returns errTimeout and any byte that later
// arrives is buffered for the next read, so nothing is lost.
func (r *osKeyReader) peekByte() (byte, error) {
	r.mu.Lock()
	if len(r.pending) > 0 {
		b := r.pending[0]
		r.mu.Unlock()
		return b, nil
	}
	r.mu.Unlock()

	type result struct {
		b   byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, 1)
		n, err := r.file.Read(buf)
		if err != nil {
			done <- result{err: err}
			return
		}
		if n == 0 {
			done <- result{err: io.EOF}
			return
		}
		r.mu.Lock()
		r.pending = append(r.pending, buf[0])
		r.mu.Unlock()
		done <- result{b: buf[0]}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			return 0, res.err
		}
		// The byte stays in pending: this is a peek, and every caller
		// (readEscape, readCSI) consumes it explicitly with readByte
		// afterwards. Popping it here made peekByte consume on a fresh read
		// but not on a buffered one, so readCSI's "consume '['" ate the final
		// byte of the sequence instead and every arrow key blocked waiting for
		// the next keystroke.
		return res.b, nil
	case <-time.After(readTimeout):
		return 0, errTimeout
	}
}

// readEscape parses the bytes that follow an ESC byte.
func (r *osKeyReader) readEscape() (keyEvent, error) {
	nb, err := r.peekByte()
	if err != nil {
		if err == errTimeout {
			return keyEvent{}, nil // bare ESC: ignore
		}
		return keyEvent{}, err
	}

	switch nb {
	case '[':
		return r.readCSI()
	case 'O': // SS3 sequences (Home/End on some terminals)
		if _, err := r.readByte(); err != nil { // consume 'O'
			return keyEvent{}, err
		}
		final, err := r.readByte()
		if err != nil {
			return keyEvent{}, err
		}
		switch final {
		case 'H':
			return keyEvent{code: keyHome}, nil
		case 'F':
			return keyEvent{code: keyEnd}, nil
		}
		return keyEvent{}, nil
	default:
		// Alt+key: report the underlying key. The only Alt combination the
		// editor acts on is Alt+Enter (ESC CR), which becomes a newline.
		if _, err := r.readByte(); err != nil { // consume the key byte
			return keyEvent{}, err
		}
		ev := decodeByte(nb)
		if ev.code == keyEnter {
			return keyEvent{code: keyNewline}, nil
		}
		return keyEvent{}, nil
	}
}

// readCSI parses a Control Sequence Introducer sequence: ESC [ params final.
func (r *osKeyReader) readCSI() (keyEvent, error) {
	if _, err := r.readByte(); err != nil { // consume '['
		return keyEvent{}, err
	}

	params := make([]byte, 0, 4)
	for {
		b, err := r.readByte()
		if err != nil {
			return keyEvent{}, err
		}
		if (b >= '0' && b <= '9') || b == ';' {
			params = append(params, b)
			continue
		}
		switch b {
		case 'A':
			return keyEvent{code: keyUp}, nil
		case 'B':
			return keyEvent{code: keyDown}, nil
		case 'C':
			return keyEvent{code: keyRight}, nil
		case 'D':
			return keyEvent{code: keyLeft}, nil
		case 'H':
			return keyEvent{code: keyHome}, nil
		case 'F':
			return keyEvent{code: keyEnd}, nil
		case 'Z':
			return keyEvent{code: keyTab}, nil // Shift+Tab treated as Tab
		case '~':
			switch string(params) {
			case "1", "7":
				return keyEvent{code: keyHome}, nil
			case "3":
				return keyEvent{code: keyDelete}, nil
			case "4", "8":
				return keyEvent{code: keyEnd}, nil
			}
			return keyEvent{}, nil
		}
		return keyEvent{}, nil
	}
}

var errTimeout = &timeoutError{}

type timeoutError struct{}

func (*timeoutError) Error() string { return "terminal read timed out" }

// decodeByte maps a single byte to a keyEvent.
func decodeByte(b byte) keyEvent {
	switch b {
	case '\r', '\n':
		return keyEvent{code: keyEnter}
	case '\t':
		return keyEvent{code: keyTab}
	case 0x7f, 0x08:
		return keyEvent{code: keyBackspace}
	case 0x03:
		return keyEvent{code: keyCtrlC}
	case 0x04:
		return keyEvent{code: keyCtrlD}
	case 0x01:
		return keyEvent{code: keyHome}
	case 0x05:
		return keyEvent{code: keyEnd}
	default:
		if b >= 0x20 && b <= 0x7e {
			return keyEvent{r: rune(b)}
		}
		return keyEvent{}
	}
}

// makeRaw switches the terminal into raw mode, returning the previous state so
// the caller can restore it. fd must be a terminal.
func makeRaw(fd int) (*term.State, error) {
	return term.MakeRaw(fd)
}

// restoreTerminal returns a terminal to its pre-raw state.
func restoreTerminal(fd int, old *term.State) {
	if old != nil {
		_ = term.Restore(fd, old)
	}
}

// terminalSize returns the terminal dimensions for rendering.
func terminalSize(fd int) (width, height int) {
	w, h, err := term.GetSize(fd)
	if err != nil {
		return 80, 24
	}
	return w, h
}
