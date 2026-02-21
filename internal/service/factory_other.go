//go:build !linux && !darwin

package service

func newSystemdManager(cfg Config) (Manager, error) {
	return nil, fmt.Errorf("systemd not available on this platform")
}

func newLaunchdManager(cfg Config) (Manager, error) {
	return nil, fmt.Errorf("launchd not available on this platform")
}

func newUnsupportedManager() (Manager, error) {
	return newUnsupported()
}
