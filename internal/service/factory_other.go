//go:build !linux && !darwin

package service

import "fmt"

func newUnsupportedManager() (Manager, error) {
	return nil, fmt.Errorf("unsupported platform")
}

func NewManager(cfg Config) (Manager, error) {
	return newUnsupportedManager()
}
