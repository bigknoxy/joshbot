//go:build linux

package main

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// openPty returns a master/slave pair. The slave is a real terminal, so
// makeRaw can be exercised against one.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no ptmx available: %v", err)
	}
	var unlock int32
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, int(unlock)); err != nil {
		master.Close()
		t.Skipf("TIOCSPTLCK failed: %v", err)
	}
	ptn, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Skipf("TIOCGPTN failed: %v", err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", ptn), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Skipf("cannot open the pty slave: %v", err)
	}
	return master, slave
}

// The property that matters is on the real terminal, not in a helper: after
// makeRaw, a written "\n" must still return the carriage. A pty is the only
// way to check it in a test.
func TestMakeRawOnARealTerminalKeepsTheCarriageReturn(t *testing.T) {
	master, slave := openPty(t)
	defer master.Close()
	defer slave.Close()

	old, err := makeRaw(int(slave.Fd()))
	if err != nil {
		t.Fatalf("makeRaw: %v", err)
	}
	defer restoreTerminal(int(slave.Fd()), old)

	got, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("read termios: %v", err)
	}
	if got.Oflag&unix.OPOST == 0 {
		t.Error("OPOST is cleared on the live terminal: \\n would not become \\r\\n")
	}
	if got.Oflag&unix.ONLCR == 0 {
		t.Error("ONLCR is cleared on the live terminal: the staircase of #503 is back")
	}
	if got.Lflag&unix.ICANON != 0 {
		t.Error("ICANON is still set: the input is not raw")
	}
	if got.Lflag&unix.ECHO != 0 {
		t.Error("ECHO is still set: the editor's keystrokes would be echoed twice")
	}
}
