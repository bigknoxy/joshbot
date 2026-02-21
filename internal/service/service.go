// Package service provides system service management for joshbot.
package service

import "runtime"

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
