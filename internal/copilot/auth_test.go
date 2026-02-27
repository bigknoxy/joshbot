package copilot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetHomeDir(t *testing.T) {
	home, err := GetHomeDir()
	if err != nil {
		t.Fatalf("GetHomeDir() error = %v", err)
	}
	if home == "" {
		t.Fatal("GetHomeDir() returned empty string")
	}
	// Verify it matches os.UserHomeDir()
	expected, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir() error = %v", err)
	}
	if home != expected {
		t.Errorf("GetHomeDir() = %v, want %v", home, expected)
	}
}

func TestAuthFilePath(t *testing.T) {
	authPath, err := AuthFilePath()
	if err != nil {
		t.Fatalf("AuthFilePath() error = %v", err)
	}
	if authPath == "" {
		t.Fatal("AuthFilePath() returned empty string")
	}
	// Verify it ends with .joshbot/auth.json
	expected := filepath.Join(".", ".joshbot", "auth.json")
	// Get the home directory to compare
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir() error = %v", err)
	}
	expected = filepath.Join(home, ".joshbot", "auth.json")
	if authPath != expected {
		t.Errorf("AuthFilePath() = %v, want %v", authPath, expected)
	}
}

func TestLoadTokenWithCorrectPath(t *testing.T) {
	// This test verifies that LoadToken expects a home directory (~),
	// not a joshbot config directory (~/.joshbot)
	home, err := GetHomeDir()
	if err != nil {
		t.Fatalf("GetHomeDir() error = %v", err)
	}

	// LoadToken should receive home directory, not config directory
	// This test just verifies the function works with the correct input
	_, err = LoadToken(home)
	// We expect either no token (not authenticated) or an error,
	// but not a path error
	if err != nil && err.Error() == "failed to read auth file: open "+filepath.Join(home, ".joshbot", ".joshbot", "auth.json")+": no such file or directory" {
		t.Errorf("LoadToken() is looking for wrong path (double .joshbot): %v", err)
	}
}
