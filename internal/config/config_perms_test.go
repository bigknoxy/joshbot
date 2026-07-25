package config

import (
	"os"
	"path/filepath"
	"testing"
)

// config.json holds live provider API keys. Writing it world-readable means
// any other account on the machine can read them, and it makes the shell
// tool's own containment irrelevant — the file is readable by anything.
func TestSave_ConfigIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	oldHome := DefaultHome
	DefaultHome = dir
	t.Cleanup(func() { DefaultHome = oldHome })

	cfg := Defaults()
	cfg.Providers = map[string]ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-or-v1-secret"},
	}

	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	perm := info.Mode().Perm()
	if perm&0o077 != 0 {
		t.Errorf("config.json mode is %#o; group/other must have no access on a file holding API keys", perm)
	}
	if perm&0o400 == 0 {
		t.Errorf("config.json mode is %#o; the owner must still be able to read it", perm)
	}
}
