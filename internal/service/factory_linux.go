//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func newSystemdManager(cfg Config) (Manager, error) {
	return newSystemd(cfg)
}

func newOpenRCManager(cfg Config) (Manager, error) {
	return newOpenRC(cfg)
}

func newLaunchdManager(cfg Config) (Manager, error) {
	return nil, fmt.Errorf("launchd not available on linux")
}

func newUnsupportedManager() (Manager, error) {
	return nil, fmt.Errorf("unsupported platform")
}

func NewManager(cfg Config) (Manager, error) {
	if hasCommand("systemctl") {
		return newSystemdManager(cfg)
	}

	if isAlpineLinux() || hasCommand("rc-update") {
		return newOpenRCManager(cfg)
	}

	return nil, ErrSystemdNotDetected
}

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func isAlpineLinux() bool {
	content, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}

	osRelease := strings.ToLower(string(content))
	return strings.Contains(osRelease, "id=alpine") || strings.Contains(osRelease, "id_like=alpine")
}
