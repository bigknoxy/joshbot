//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrSystemdNotDetected is returned when systemd is not available on the system.
var ErrSystemdNotDetected = fmt.Errorf("systemd not detected. On Alpine/OpenRC systems use an OpenRC service, a crond @reboot entry, or run joshbot in a container with --restart unless-stopped")

// checkSystemctl checks if systemctl exists in PATH.
func checkSystemctl() error {
	_, err := exec.LookPath("systemctl")
	if err != nil {
		return ErrSystemdNotDetected
	}
	return nil
}

type systemdManager struct {
	config      Config
	servicePath string
	isRoot      bool
}

func newSystemd(cfg Config) (*systemdManager, error) {
	// Check if systemctl is available before proceeding
	if err := checkSystemctl(); err != nil {
		return nil, err
	}

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
	if cfg.ExecPath == "" {
		execPath, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("failed to detect executable path: %w", err)
		}
		cfg.ExecPath = execPath
	}

	return &systemdManager{
		config:      cfg,
		servicePath: fmt.Sprintf("/etc/systemd/system/%s.service", cfg.Name),
		isRoot:      os.Geteuid() == 0,
	}, nil
}

func (s *systemdManager) runCommand(name string, args ...string) error {
	cmd := s.buildCommand(name, args...)
	return cmd.Run()
}

func (s *systemdManager) runCommandOutput(name string, args ...string) ([]byte, error) {
	cmd := s.buildCommand(name, args...)
	return cmd.Output()
}

func (s *systemdManager) runCommandCombined(name string, args ...string) ([]byte, error) {
	cmd := s.buildCommand(name, args...)
	return cmd.CombinedOutput()
}

// buildCommand constructs an exec.Cmd, using sudo if not running as root.
func (s *systemdManager) buildCommand(name string, args ...string) *exec.Cmd {
	if s.isRoot {
		return exec.Command(name, args...)
	}
	return exec.Command("sudo", append([]string{name}, args...)...)
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

	// Get the current user's home directory for HOME environment variable
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = s.config.WorkingDir // Fallback to working directory
	}

	unit := fmt.Sprintf(`[Unit]
Description=%s
After=network.target

[Service]
Type=simple
ExecStart=%s gateway
WorkingDirectory=%s
Environment=HOME=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, s.config.DisplayName, s.config.ExecPath, s.config.WorkingDir, homeDir)

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

func (s *systemdManager) Restart() error {
	if !s.IsInstalled() {
		return fmt.Errorf("service not installed")
	}
	return s.runCommand("systemctl", "restart", s.config.Name)
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
