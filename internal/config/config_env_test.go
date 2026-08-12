package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetAllModelConfigsEmpty(t *testing.T) {
	cfg := Defaults()
	if got := cfg.GetAllModelConfigs(); len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}

func TestLoadStrictFromMissingFile(t *testing.T) {
	savedHome := DefaultHome
	t.Cleanup(func() { SetHome(savedHome) })

	cfg, err := LoadStrictFrom(filepath.Join(t.TempDir(), "missing.json"))
	if cfg != nil {
		t.Error("cfg must be nil when file is missing")
	}
	if !strings.Contains(err.Error(), "not found") && !os.IsNotExist(err) {
		t.Errorf("expected not-exist error: %v", err)
	}
}

func TestLoadStrictFromBadJSON(t *testing.T) {
	savedHome := DefaultHome
	t.Cleanup(func() { SetHome(savedHome) })

	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte{0x7b, 0x21}, 0o644)
	cfg, err := LoadStrictFrom(path)
	if cfg == nil {
		t.Error("still returns Defaults() even on parse error")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("should name the parse error: %v", err)
	}
}

func TestFatalConfigErrorUnwrap(t *testing.T) {
	inner := &os.PathError{Op: "open", Path: "test.json", Err: os.ErrNotExist}
	wrapped := fatalConfigError{error: inner}
	if wrapped.Unwrap() != inner {
		t.Error("Unwrap must return the original error")
	}
}

func TestPoolsideModelIDPreserved(t *testing.T) {
	got := StripProviderPrefix("poolside/laguna-m.1")
	if !strings.HasPrefix(got, "poolside/") {
		t.Errorf("should not strip poolside prefix: got %s", got)
	}
}
