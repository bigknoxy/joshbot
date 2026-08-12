package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// The self-update path replaces the running binary. Its helpers are small, but
// each failure mode here ends with a user holding a broken install: a truncated
// download written over the binary, or a 404 for a platform that has no
// artifact reported as success.

func TestDownloadBinaryWritesTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Errorf("download sent no User-Agent; GitHub rate-limits anonymous clients harder")
		}
		_, _ = w.Write([]byte("binary-bytes"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "joshbot.new")
	if err := downloadBinary(srv.URL, dest); err != nil {
		t.Fatalf("downloadBinary: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "binary-bytes" {
		t.Errorf("downloaded %q, want %q", got, "binary-bytes")
	}
}

// A 404 is the shape of "no release artifact for this platform", and must say
// so rather than leaving a zero-byte file that later gets copied over the
// installed binary.
func TestDownloadBinary404IsAPlatformError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "joshbot.new")
	err := downloadBinary(srv.URL, dest)
	if err == nil {
		t.Fatal("expected an error on 404")
	}
	if !strings.Contains(err.Error(), "platform") && !strings.Contains(err.Error(), "not found") {
		t.Errorf("404 error should explain there is no artifact for this platform, got: %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a failed download left a file behind; the update path would copy it over the binary")
	}
}

func TestDownloadBinaryNon200IsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := downloadBinary(srv.URL, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("expected an error on a 500")
	}
}

func TestDownloadBinaryNetworkErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	if err := downloadBinary(url, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("expected a network error against a closed server")
	}
}

func TestCopyFileCopiesContents(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("payload"), 0600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("copied %q, want %q", got, "payload")
	}
}

// copyFile must report a missing source rather than silently creating an empty
// destination — the destination is the installed binary.
func TestCopyFileMissingSourceIsAnError(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst")

	if err := copyFile(filepath.Join(dir, "nope"), dst); err == nil {
		t.Fatal("expected an error for a missing source")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("copyFile created the destination even though the source did not exist")
	}
}

// getBinaryPath resolves symlinks, because the installed joshbot is commonly a
// symlink and the update path must replace the real file.
func TestGetBinaryPathResolvesToARealFile(t *testing.T) {
	p, err := getBinaryPath()
	if err != nil {
		t.Fatalf("getBinaryPath: %v", err)
	}
	if p == "" || !filepath.IsAbs(p) {
		t.Fatalf("getBinaryPath returned %q, want an absolute path", p)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("lstat %q: %v", p, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("getBinaryPath returned a symlink (%q); symlinks must be resolved", p)
	}
}

// getChannelID has to cope with whichever numeric type the JSON decoder or the
// channel implementation produced. A chat id that comes back as "" routes the
// reply nowhere.
func TestGetChannelIDAcceptsEveryNumericShape(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want string
	}{
		{"string", "12345", "12345"},
		{"int64", int64(12345), "12345"},
		{"float64 from JSON", float64(12345), "12345"},
		{"int", 12345, "12345"},
		{"unsupported type yields empty", []string{"12345"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := bus.InboundMessage{Metadata: map[string]any{"chat_id": tc.val}}
			if got := getChannelID(msg); got != tc.want {
				t.Errorf("getChannelID(%v) = %q, want %q", tc.val, got, tc.want)
			}
		})
	}

	if got := getChannelID(bus.InboundMessage{}); got != "" {
		t.Errorf("getChannelID with no metadata = %q, want empty", got)
	}
}

// applyNoColor must be safe to call before anything else has configured the
// logger; it runs from the root Before hook (issue #148).
func TestApplyNoColorIsSafeToCall(t *testing.T) {
	applyNoColor()
	applyNoColor()
}

// detectRunningContext must not report `go run` for a test binary living under
// a temp dir — the /tmp substring check it replaced did exactly that.
func TestDetectRunningContextDoesNotClaimGoRunForATestBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	if runningFromGoRun(exe) {
		t.Skip("the test binary really is under the go-build cache")
	}
	if ctx := detectRunningContext(); ctx.IsGoRun {
		t.Errorf("detectRunningContext reported IsGoRun for %q", exe)
	}
}
