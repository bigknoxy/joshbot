//go:build !linux && !darwin

package service

import (
	"fmt"
	"runtime"
)

type unsupportedManager struct{}

func newUnsupported() (Manager, error) {
	return &unsupportedManager{}, nil
}

func (s *unsupportedManager) Name() string {
	return "unsupported"
}

func (s *unsupportedManager) IsInstalled() bool {
	return false
}

func (s *unsupportedManager) Install() (Result, error) {
	return Result{}, fmt.Errorf("service management not supported on %s", runtime.GOOS)
}

func (s *unsupportedManager) Uninstall() (Result, error) {
	return Result{}, fmt.Errorf("service management not supported on %s", runtime.GOOS)
}

func (s *unsupportedManager) Start() error {
	return fmt.Errorf("service management not supported on %s", runtime.GOOS)
}

func (s *unsupportedManager) Stop() error {
	return fmt.Errorf("service management not supported on %s", runtime.GOOS)
}

func (s *unsupportedManager) Status() (Status, error) {
	return Status{}, fmt.Errorf("service management not supported on %s", runtime.GOOS)
}
