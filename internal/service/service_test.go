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
