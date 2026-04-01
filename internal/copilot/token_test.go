package copilot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadToken(t *testing.T) {
	tmpDir := t.TempDir()

	token := &TokenInfo{
		AccessToken:  "test-token-123",
		RefreshToken: "refresh-456",
		ExpiresAt:    0, // No expiry
	}

	err := SaveToken(tmpDir, token)
	if err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	// Verify file exists
	authFile := filepath.Join(tmpDir, ".joshbot", "auth.json")
	if _, err := os.Stat(authFile); os.IsNotExist(err) {
		t.Fatal("Auth file was not created")
	}

	// Load and verify
	loaded, err := LoadToken(tmpDir)
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadToken() returned nil")
	}
	if loaded.AccessToken != "test-token-123" {
		t.Errorf("Loaded token = %q, want %q", loaded.AccessToken, "test-token-123")
	}
}

func TestLoadTokenMissingFile(t *testing.T) {
	tmpDir := t.TempDir()

	token, err := LoadToken(tmpDir)
	if err != nil {
		t.Fatalf("LoadToken() with missing file error = %v", err)
	}
	if token != nil {
		t.Error("LoadToken() should return nil token for missing file")
	}
}

func TestLoadTokenExpired(t *testing.T) {
	tmpDir := t.TempDir()

	token := &TokenInfo{
		AccessToken: "expired-token",
		ExpiresAt:   1000000000, // Expired in 2001
	}

	err := SaveToken(tmpDir, token)
	if err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	_, err = LoadToken(tmpDir)
	if err == nil {
		t.Error("LoadToken() should return error for expired token")
	}
}

func TestLoadTokenInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, ".joshbot")
	os.MkdirAll(authDir, 0700)

	authFile := filepath.Join(authDir, "auth.json")
	os.WriteFile(authFile, []byte("not valid json"), 0600)

	_, err := LoadToken(tmpDir)
	if err == nil {
		t.Error("LoadToken() should return error for invalid JSON")
	}
}

func TestIsAuthError(t *testing.T) {
	tests := []struct {
		errStr    string
		isAuthErr bool
	}{
		{"authorization expired, please run auth again", true},
		{"authorization denied", true},
		{"authorization_pending", false},
		{"network error", false},
		{"", false},
	}

	for _, tt := range tests {
		var err error
		if tt.errStr != "" {
			err = &testError{tt.errStr}
		}
		got := isAuthError(err)
		if got != tt.isAuthErr {
			t.Errorf("isAuthError(%q) = %v, want %v", tt.errStr, got, tt.isAuthErr)
		}
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
