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
	isRoot      bool
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
		isRoot:      os.Geteuid() == 0,
	}, nil
}

func (s *systemdManager) runCommand(name string, args ...string) error {
	var cmd *exec.Cmd
	if s.isRoot {
		cmd = exec.Command(name, args...)
	} else {
		cmd = exec.Command("sudo", append([]string{name}, args...)...)
	}
	return cmd.Run()
}

func (s *systemdManager) runCommandOutput(name string, args ...string) ([]byte, error) {
	var cmd *exec.Cmd
	if s.isRoot {
		cmd = exec.Command(name, args...)
	} else {
		cmd = exec.Command("sudo", append([]string{name}, args...)...)
	}
	return cmd.Output()
}

func (s *systemdManager) runCommandCombined(name string, args ...string) ([]byte, error) {
	var cmd *exec.Cmd
	if s.isRoot {
		cmd = exec.Command(name, args...)
	} else {
		cmd = exec.Command("sudo", append([]string{name}, args...)...)
	}
	return cmd.CombinedOutput()
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

	if err := s.runCommand("cp", tmpFile.Name(), s.servicePath); err != nil {
		return Result{}, fmt.Errorf("failed to copy service file: %w", err)
	}

	if err := s.runCommand("systemctl", "daemon-reload"); err != nil {
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
		if err := s.runCommand("systemctl", "stop", s.config.Name); err != nil {
			return Result{}, fmt.Errorf("failed to stop service: %w", err)
		}
	}

	if err := s.runCommand("systemctl", "disable", s.config.Name); err != nil {
		return Result{}, fmt.Errorf("failed to disable service: %w", err)
	}

	if err := s.runCommand("rm", s.servicePath); err != nil {
		return Result{}, fmt.Errorf("failed to remove service file: %w", err)
	}

	if err := s.runCommand("systemctl", "daemon-reload"); err != nil {
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

	if err := s.runCommand("systemctl", "enable", s.config.Name); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	if err := s.runCommand("systemctl", "start", s.config.Name); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	return nil
}

func (s *systemdManager) Stop() error {
	if !s.IsInstalled() {
		return fmt.Errorf("service not installed")
	}

	return s.runCommand("systemctl", "stop", s.config.Name)
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
