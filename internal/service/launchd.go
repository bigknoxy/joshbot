//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type launchdManager struct {
	config    Config
	plistPath string
	logPath   string
	errorPath string
}

func newLaunchd(cfg Config) (Manager, error) {
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

	home, _ := os.UserHomeDir()
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	logDir := filepath.Join(home, ".joshbot", "logs")

	return &launchdManager{
		config:    cfg,
		plistPath: filepath.Join(agentsDir, fmt.Sprintf("dev.joshbot.%s.plist", cfg.Name)),
		logPath:   filepath.Join(logDir, "joshbot.log"),
		errorPath: filepath.Join(logDir, "joshbot.error.log"),
	}, nil
}

func (s *launchdManager) Name() string {
	return "launchd"
}

// serviceID is the launchd Label, and must match the <string> under <key>Label</key>
// in the installed plist or bootout/list can never find the job.
func (s *launchdManager) serviceID() string {
	return fmt.Sprintf("dev.joshbot.%s", s.config.Name)
}

// domainTarget is the launchd domain the job lives in. `bootstrap` and `bootout`
// both require the UID here: a bare "gui" is not a domain target and launchctl
// rejects it, so the service never starts.
func (s *launchdManager) domainTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// serviceTarget is the domain-qualified job name, the argument form `bootout` takes.
func (s *launchdManager) serviceTarget() string {
	return fmt.Sprintf("%s/%s", s.domainTarget(), s.serviceID())
}

func (s *launchdManager) bootstrapArgs() []string {
	return []string{"bootstrap", s.domainTarget(), s.plistPath}
}

func (s *launchdManager) bootoutArgs() []string {
	return []string{"bootout", s.serviceTarget()}
}

func (s *launchdManager) IsInstalled() bool {
	_, err := os.Stat(s.plistPath)
	return err == nil
}

func (s *launchdManager) Install() (Result, error) {
	if s.IsInstalled() {
		return Result{}, fmt.Errorf("service already installed at %s", s.plistPath)
	}

	home, _ := os.UserHomeDir()
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return Result{}, fmt.Errorf("failed to create LaunchAgents directory: %w", err)
	}

	logDir := filepath.Dir(s.logPath)
	// The service log holds conversation content; ~/Library/LaunchAgents
	// above is a shared OS location and keeps its conventional mode.
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return Result{}, fmt.Errorf("failed to create logs directory: %w", err)
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.joshbot.%s</string>
    <key>DisplayName</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>gateway</string>
    </array>
    <key>WorkingDirectory</key>
    <string>%s</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>%s</string>
    </dict>
</dict>
</plist>
`, s.config.Name, s.config.DisplayName, s.config.ExecPath, s.config.WorkingDir, s.logPath, s.errorPath, home)

	if err := os.WriteFile(s.plistPath, []byte(plist), 0600); err != nil {
		return Result{}, fmt.Errorf("failed to write plist: %w", err)
	}

	return Result{
		Success: true,
		Message: "Service installed successfully",
		LogPath: s.logPath,
	}, nil
}

func (s *launchdManager) Uninstall() (Result, error) {
	if !s.IsInstalled() {
		return Result{}, fmt.Errorf("service not installed")
	}

	if s.isRunning() {
		if err := exec.Command("launchctl", s.bootoutArgs()...).Run(); err != nil {
			return Result{}, fmt.Errorf("failed to unload service: %w", err)
		}
	}

	if err := os.Remove(s.plistPath); err != nil {
		return Result{}, fmt.Errorf("failed to remove plist: %w", err)
	}

	return Result{
		Success: true,
		Message: "Service uninstalled successfully",
	}, nil
}

func (s *launchdManager) Start() error {
	if !s.IsInstalled() {
		return fmt.Errorf("service not installed")
	}

	return exec.Command("launchctl", s.bootstrapArgs()...).Run()
}

func (s *launchdManager) Stop() error {
	if !s.IsInstalled() {
		return fmt.Errorf("service not installed")
	}

	return exec.Command("launchctl", s.bootoutArgs()...).Run()
}

func (s *launchdManager) Restart() error {
	if !s.IsInstalled() {
		return fmt.Errorf("service not installed")
	}
	// launchd doesn't have direct restart, so stop then start
	if s.isRunning() {
		if err := s.Stop(); err != nil {
			return fmt.Errorf("failed to stop service: %w", err)
		}
	}
	return s.Start()
}

func (s *launchdManager) Status() (Status, error) {
	status := Status{
		Installed: s.IsInstalled(),
	}

	if !status.Installed {
		return status, nil
	}

	status.Running = s.isRunning()

	if status.Running {
		status.Status = "running"
	} else {
		status.Status = "stopped"
	}

	return status, nil
}

func (s *launchdManager) isRunning() bool {
	serviceID := s.serviceID()
	out, err := exec.Command("launchctl", "list").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), serviceID)
}
