package service

import (
	"runtime"
	"testing"
)

func TestPlatform(t *testing.T) {
	platform := Platform()

	switch runtime.GOOS {
	case "linux":
		if platform != "systemd" {
			t.Errorf("Platform() on linux = %q, want %q", platform, "systemd")
		}
	case "darwin":
		if platform != "launchd" {
			t.Errorf("Platform() on darwin = %q, want %q", platform, "launchd")
		}
	default:
		if platform != "unsupported" {
			t.Errorf("Platform() on %s = %q, want %q", runtime.GOOS, platform, "unsupported")
		}
	}
}

func TestConfigStruct(t *testing.T) {
	cfg := Config{
		Name:        "joshbot",
		DisplayName: "Joshbot Service",
		Description: "Personal AI assistant",
		ExecPath:    "/usr/local/bin/joshbot",
		WorkingDir:  "/home/user",
	}

	if cfg.Name != "joshbot" {
		t.Errorf("Config.Name = %q, want %q", cfg.Name, "joshbot")
	}
	if cfg.DisplayName != "Joshbot Service" {
		t.Errorf("Config.DisplayName = %q, want %q", cfg.DisplayName, "Joshbot Service")
	}
}

func TestStatusStruct(t *testing.T) {
	status := Status{
		Running:   true,
		Installed: true,
		Status:    "active",
	}

	if !status.Running {
		t.Error("Status.Running should be true")
	}
	if !status.Installed {
		t.Error("Status.Installed should be true")
	}
	if status.Status != "active" {
		t.Errorf("Status.Status = %q, want %q", status.Status, "active")
	}
}

func TestResultStruct(t *testing.T) {
	result := Result{
		Message: "Service installed",
		LogPath: "/var/log/joshbot.log",
		Success: true,
	}

	if !result.Success {
		t.Error("Result.Success should be true")
	}
	if result.Message != "Service installed" {
		t.Errorf("Result.Message = %q, want %q", result.Message, "Service installed")
	}
}
