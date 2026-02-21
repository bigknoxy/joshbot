//go:build darwin

package service

import "fmt"

func newSystemdManager(cfg Config) (Manager, error) {
	return nil, fmt.Errorf("systemd not available on darwin")
}

func newLaunchdManager(cfg Config) (Manager, error) {
	return newLaunchd(cfg)
}

func newUnsupportedManager() (Manager, error) {
	return nil, fmt.Errorf("unsupported platform")
}
