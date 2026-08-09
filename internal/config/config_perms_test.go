package config

import (
	"os"
	"path/filepath"
	"strings"
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

// Every directory joshbot creates under ~/.joshbot must be owner-only.
//
// MkdirAll leaves an existing directory's mode alone, so onboarding creating
// the tree at 0755 silently won over the 0750 session.NewManager asks for:
// session files were 0600 while the directory listing them was world-readable,
// and a session file is named "telegram:<senderID>".
func TestJoshbotHomeDirectoriesAreOwnerOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".joshbot")
	oldHome, oldWs := DefaultHome, DefaultWorkspace
	DefaultHome = root
	DefaultWorkspace = filepath.Join(root, "workspace")
	t.Cleanup(func() { DefaultHome, DefaultWorkspace = oldHome, oldWs })

	cfg := Defaults()
	cfg.Agents.Defaults.Workspace = DefaultWorkspace

	// Pre-create one directory world-readable, standing in for an install
	// made before this was tightened. MkdirAll would leave it alone.
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0o755); err != nil {
		t.Fatalf("seed stale dir: %v", err)
	}

	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return err
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Errorf("%s is mode %04o; group/other must have no access",
				strings.TrimPrefix(path, root), got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// The config package logs before joshbot installs its own redacting logger, so
// its fallback logger has to redact on its own. `joshbot configure` printed the
// operator's full home path this way while every other command printed "~".
func TestDefaultLoggerRedacts(t *testing.T) {
	t.Setenv("HOME", "/home/someaccount")
	d := &defaultLogger{}

	out := d.format("INFO", "Config saved", "path", "/home/someaccount/.joshbot/config.json")
	if strings.Contains(out, "someaccount") {
		t.Errorf("home path was not redacted: %q", out)
	}
	if !strings.Contains(out, "~/.joshbot/config.json") {
		t.Errorf("expected a ~-rooted path, got %q", out)
	}

	secret := d.format("WARN", "provider", "api_key", "sk-or-v1-abcdefghijklmnopqrst")
	if strings.Contains(secret, "sk-or-v1-abcdefghijklmnopqrst") {
		t.Errorf("credential was not redacted: %q", secret)
	}
}
