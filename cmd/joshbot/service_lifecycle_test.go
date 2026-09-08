package main

import (
	"fmt"
	"testing"

	"github.com/bigknoxy/joshbot/internal/service"
	"github.com/urfave/cli/v2"
)

// fakeServiceManager implements service.Manager for CLI testing without
// touching the real system service manager.
type fakeServiceManager struct {
	installed   bool
	running     bool
	startCalled bool
	stopCalled  bool
	restartErr  error
}

func (f *fakeServiceManager) Install() (service.Result, error) {
	f.installed = true
	return service.Result{Success: true, Message: "installed"}, nil
}
func (f *fakeServiceManager) Uninstall() (service.Result, error) {
	f.installed = false
	return service.Result{Success: true, Message: "uninstalled"}, nil
}
func (f *fakeServiceManager) Status() (service.Status, error) {
	return service.Status{Installed: f.installed, Running: f.running}, nil
}
func (f *fakeServiceManager) Start() error {
	if !f.installed {
		return fmt.Errorf("service not installed")
	}
	f.startCalled = true
	f.running = true
	return nil
}
func (f *fakeServiceManager) Stop() error {
	if !f.installed {
		return fmt.Errorf("service not installed")
	}
	f.stopCalled = true
	f.running = false
	return nil
}
func (f *fakeServiceManager) Restart() error {
	return f.restartErr
}
func (f *fakeServiceManager) IsInstalled() bool { return f.installed }
func (f *fakeServiceManager) Name() string      { return "fake" }

var errTest = fmt.Errorf("test error")

func withFakeManager(t *testing.T, fake *fakeServiceManager) {
	t.Helper()
	prev := newServiceManager
	newServiceManager = func(cfg service.Config) (service.Manager, error) { return fake, nil }
	t.Cleanup(func() { newServiceManager = prev })
}

func TestRunServiceStart(t *testing.T) {
	fake := &fakeServiceManager{installed: true}
	withFakeManager(t, fake)

	if err := runServiceStart(&cli.Context{}); err != nil {
		t.Fatalf("service start: %v", err)
	}
	if !fake.startCalled {
		t.Fatal("Start was not called")
	}
	if !fake.running {
		t.Fatal("Start should have set running")
	}
}

func TestRunServiceStartNotInstalled(t *testing.T) {
	fake := &fakeServiceManager{installed: false}
	withFakeManager(t, fake)

	if err := runServiceStart(&cli.Context{}); err == nil {
		t.Fatal("expected error when service not installed")
	}
}

func TestRunServiceStop(t *testing.T) {
	fake := &fakeServiceManager{installed: true, running: true}
	withFakeManager(t, fake)

	if err := runServiceStop(&cli.Context{}); err != nil {
		t.Fatalf("service stop: %v", err)
	}
	if !fake.stopCalled {
		t.Fatal("Stop was not called")
	}
}

func TestRunServiceRestart(t *testing.T) {
	fake := &fakeServiceManager{installed: true, running: true}
	withFakeManager(t, fake)

	if err := runServiceRestart(&cli.Context{}); err != nil {
		t.Fatalf("service restart: %v", err)
	}
}

func TestRunServiceRestartError(t *testing.T) {
	fake := &fakeServiceManager{installed: true, running: true, restartErr: errTest}
	withFakeManager(t, fake)

	if err := runServiceRestart(&cli.Context{}); err == nil {
		t.Fatal("expected error from failed restart")
	}
}
