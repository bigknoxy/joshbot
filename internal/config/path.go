package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Where joshbot reads and writes its configuration.
//
// The home directory and the config file were previously coupled by
// convention alone: callers wanting a specific file overrode DefaultHome to
// the file's directory, loaded "config.json" from it, and put the global back
// afterwards. That discarded the file name the caller had asked for, and left
// everything derived from the home — sessions, media, cron, the skills trust
// store, and Save itself — pointing at the original location. Reading one file
// and writing another is the kind of failure a user cannot see.

// activeConfigPath is the config file in use. Empty means "derive it from
// DefaultHome", which is the ordinary case.
var activeConfigPath string

// ConfigPath returns the config file joshbot reads and writes.
func ConfigPath() string {
	if activeConfigPath != "" {
		return activeConfigPath
	}
	return filepath.Join(DefaultHome, "config.json")
}

// SetHome points joshbot at a different home directory, recomputing everything
// derived from it.
//
// DefaultWorkspace is a package variable computed at init, so moving the home
// without updating it leaves the workspace behind — the flag would be half
// applied, which is worse than not applying it.
func SetHome(dir string) {
	DefaultHome = dir
	DefaultWorkspace = filepath.Join(dir, "workspace")
	activeConfigPath = ""
}

// LoadFrom loads configuration from an explicit file path.
//
// The file must exist: falling back to defaults would leave the caller
// believing their file had been read. Loading also anchors the home directory
// to the file's parent, so sessions, media, cron and the skills trust store
// live alongside the config that selected them rather than being split across
// two homes.
func LoadFrom(path string) (*Config, error) {
	if path == "" {
		return Load()
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path %s: %w", path, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found: %s", abs)
		}
		return nil, fmt.Errorf("config file %s: %w", abs, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("config path %s is a directory; give the path to a config file", abs)
	}

	SetHome(filepath.Dir(abs))
	activeConfigPath = abs

	return Load()
}
