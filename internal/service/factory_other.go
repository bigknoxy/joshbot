//go:build !linux && !darwin

package service

func NewManager(cfg Config) (Manager, error) {
	return newUnsupported()
}
