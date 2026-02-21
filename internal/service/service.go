// Package service provides system service management for joshbot.
package service

import (
	"fmt"
	"os"
	"runtime"
)

type Manager interface {
	Install() (Result, error)
	Uninstall() (Result, error)
	Status() (Status, error)
	Start() error
	Stop() error
	IsInstalled() bool
	Name() string
}

type Status struct {
	Running   bool
	Installed bool
	Status    string
}

type Result struct {
	Message string
	LogPath string
	Success bool
}

type Config struct {
	Name        string
	DisplayName string
	Description string
	ExecPath    string
	WorkingDir  string
}

func NewManager(cfg Config) (Manager, error) {
	if cfg.ExecPath == "" {
		execPath, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("failed to get executable path: %w", err)
		}
		cfg.ExecPath = execPath
	}

	switch runtime.GOOS {
	case "linux":
		return newSystemdManager(cfg)
	case "darwin":
		return newLaunchdManager(cfg)
	default:
		return newUnsupportedManager()
	}
}

func Platform() string {
	switch runtime.GOOS {
	case "linux":
		return "systemd"
	case "darwin":
		return "launchd"
	default:
		return "unsupported"
	}
}
