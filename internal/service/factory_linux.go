//go:build linux

package service

import "fmt"

func newSystemdManager(cfg Config) (Manager, error) {
	return newSystemd(cfg)
}

func newLaunchdManager(cfg Config) (Manager, error) {
	return nil, fmt.Errorf("launchd not available on linux")
}

func newUnsupportedManager() (Manager, error) {
	return nil, fmt.Errorf("unsupported platform")
}

func NewManager(cfg Config) (Manager, error) {
	return newSystemdManager(cfg)
}
