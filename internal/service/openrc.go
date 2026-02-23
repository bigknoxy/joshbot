//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type openrcManager struct {
	config     Config
	scriptPath string
	isRoot     bool
}

func newOpenRC(cfg Config) (*openrcManager, error) {
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

	return &openrcManager{
		config:     cfg,
		scriptPath: filepath.Join("/etc/init.d", cfg.Name),
		isRoot:     os.Geteuid() == 0,
	}, nil
}

func (o *openrcManager) Name() string { return "openrc" }

func (o *openrcManager) IsInstalled() bool {
	_, err := os.Stat(o.scriptPath)
	return err == nil
}

func (o *openrcManager) Install() (Result, error) {
	if o.IsInstalled() {
		return Result{}, fmt.Errorf("service already installed at %s", o.scriptPath)
	}

	script := fmt.Sprintf(`#!/sbin/openrc-run
name="%s"
description="%s"

command="%s"
command_args="gateway"
command_background=true
pidfile="/run/%s.pid"
directory="%s"

depend() {
    need net
}
`, o.config.DisplayName, o.config.Description, o.config.ExecPath, o.config.Name, o.config.WorkingDir)

	tmpFile, err := os.CreateTemp("", "joshbot-openrc-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("failed to create temp init script: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(script); err != nil {
		return Result{}, fmt.Errorf("failed to write init script: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return Result{}, fmt.Errorf("failed to close temp script: %w", err)
	}

	if err := o.runCommand("cp", tmpFile.Name(), o.scriptPath); err != nil {
		return Result{}, fmt.Errorf("failed to install init script: %w", err)
	}
	if err := o.runCommand("chmod", "755", o.scriptPath); err != nil {
		return Result{}, fmt.Errorf("failed to set init script permissions: %w", err)
	}
	if err := o.runCommand("rc-update", "add", o.config.Name, "default"); err != nil {
		return Result{}, fmt.Errorf("failed to enable service in OpenRC: %w", err)
	}

	return Result{Success: true, Message: "OpenRC service installed successfully", LogPath: "rc-service joshbot status"}, nil
}

func (o *openrcManager) Uninstall() (Result, error) {
	if !o.IsInstalled() {
		return Result{}, fmt.Errorf("service not installed")
	}

	_ = o.runCommand("rc-service", o.config.Name, "stop")
	_ = o.runCommand("rc-update", "del", o.config.Name, "default")

	if err := o.runCommand("rm", o.scriptPath); err != nil {
		return Result{}, fmt.Errorf("failed to remove init script: %w", err)
	}

	return Result{Success: true, Message: "OpenRC service uninstalled successfully"}, nil
}

func (o *openrcManager) Start() error {
	if !o.IsInstalled() {
		return fmt.Errorf("service not installed")
	}
	return o.runCommand("rc-service", o.config.Name, "start")
}

func (o *openrcManager) Stop() error {
	if !o.IsInstalled() {
		return fmt.Errorf("service not installed")
	}
	return o.runCommand("rc-service", o.config.Name, "stop")
}

func (o *openrcManager) Status() (Status, error) {
	if !o.IsInstalled() {
		return Status{Installed: false, Running: false, Status: "not installed"}, nil
	}

	out, err := exec.Command("rc-service", o.config.Name, "status").CombinedOutput()
	output := strings.ToLower(string(out))
	running := strings.Contains(output, "started") || strings.Contains(output, "status: started")
	if err != nil && !running {
		return Status{Installed: true, Running: false, Status: strings.TrimSpace(string(out))}, nil
	}

	status := "stopped"
	if running {
		status = "running"
	}
	return Status{Installed: true, Running: running, Status: status}, nil
}

func (o *openrcManager) runCommand(name string, args ...string) error {
	var cmd *exec.Cmd
	if o.isRoot {
		cmd = exec.Command(name, args...)
	} else {
		cmd = exec.Command("sudo", append([]string{name}, args...)...)
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		if len(out) > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return err
	}
	return nil
}
