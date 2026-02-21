//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type systemdManager struct {
	config      Config
	servicePath string
}

func newSystemd(cfg Config) (*systemdManager, error) {
	if cfg.Name == "" {
		cfg.Name = "joshbot"
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = "Joshbot AI Assistant"
	}
	if cfg.Description == "" {
		cfg.Description = "Personal AI assistant with Telegram integration"
	}
	if cfg.WorkingDir == "" {
		home, _ := os.UserHomeDir()
		cfg.WorkingDir = filepath.Join(home, ".joshbot")
	}

	return &systemdManager{
		config:      cfg,
		servicePath: fmt.Sprintf("/etc/systemd/system/%s.service", cfg.Name),
	}, nil
}

func (s *systemdManager) Name() string {
	return "systemd"
}

func (s *systemdManager) IsInstalled() bool {
	_, err := os.Stat(s.servicePath)
	return err == nil
}

func (s *systemdManager) Install() (Result, error) {
	if s.IsInstalled() {
		return Result{}, fmt.Errorf("service already installed at %s", s.servicePath)
	}

	unit := fmt.Sprintf(`[Unit]
Description=%s
After=network.target

[Service]
Type=simple
ExecStart=%s gateway
WorkingDirectory=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, s.config.DisplayName, s.config.ExecPath, s.config.WorkingDir)

	tmpFile, err := os.CreateTemp("", "joshbot-service-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(unit); err != nil {
		return Result{}, fmt.Errorf("failed to write service unit: %w", err)
	}
	tmpFile.Close()

	if err := exec.Command("sudo", "cp", tmpFile.Name(), s.servicePath).Run(); err != nil {
		return Result{}, fmt.Errorf("failed to copy service file (need sudo): %w", err)
	}

	if err := exec.Command("sudo", "systemctl", "daemon-reload").Run(); err != nil {
		return Result{}, fmt.Errorf("failed to reload systemd: %w", err)
	}

	return Result{
		Success: true,
		Message: "Service installed successfully!",
		LogPath: "journalctl -u joshbot -f",
	}, nil
}

func (s *systemdManager) Uninstall() (Result, error) {
	if !s.IsInstalled() {
		return Result{}, fmt.Errorf("service not installed")
	}

	if s.isRunning() {
		if err := exec.Command("sudo", "systemctl", "stop", s.config.Name).Run(); err != nil {
			return Result{}, fmt.Errorf("failed to stop service: %w", err)
		}
	}

	if err := exec.Command("sudo", "systemctl", "disable", s.config.Name).Run(); err != nil {
		return Result{}, fmt.Errorf("failed to disable service: %w", err)
	}

	if err := exec.Command("sudo", "rm", s.servicePath).Run(); err != nil {
		return Result{}, fmt.Errorf("failed to remove service file: %w", err)
	}

	if err := exec.Command("sudo", "systemctl", "daemon-reload").Run(); err != nil {
		return Result{}, fmt.Errorf("failed to reload systemd: %w", err)
	}

	return Result{
		Success: true,
		Message: "Service uninstalled successfully!",
	}, nil
}

func (s *systemdManager) Start() error {
	if !s.IsInstalled() {
		return fmt.Errorf("service not installed")
	}

	if err := exec.Command("sudo", "systemctl", "enable", s.config.Name).Run(); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	if err := exec.Command("sudo", "systemctl", "start", s.config.Name).Run(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	return nil
}

func (s *systemdManager) Stop() error {
	if !s.IsInstalled() {
		return fmt.Errorf("service not installed")
	}

	return exec.Command("sudo", "systemctl", "stop", s.config.Name).Run()
}

func (s *systemdManager) Status() (Status, error) {
	status := Status{
		Installed: s.IsInstalled(),
	}

	if !status.Installed {
		return status, nil
	}

	status.Running = s.isRunning()

	out, err := exec.Command("systemctl", "status", s.config.Name).CombinedOutput()
	if err != nil {
		status.Status = "unknown"
	} else {
		lines := strings.Split(string(out), "\n")
		if len(lines) > 2 {
			status.Status = strings.TrimSpace(lines[2])
		}
	}

	return status, nil
}

func (s *systemdManager) isRunning() bool {
	out, err := exec.Command("systemctl", "is-active", s.config.Name).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}
