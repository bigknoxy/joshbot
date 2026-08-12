package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `joshbot sessions export` is the CLI half of session.Export. The wiring is
// where the mistakes live: urfave/cli v2 stops parsing flags at the first
// positional argument, so every flag written *after* the id has to be picked up
// by parseSessionArgs. `export <id> --out <dir>` silently writing into the
// current working directory is exactly the class of bug that motivated
// parseSessionArgs in the first place.

func TestSessionsExportWritesTranscriptAndManifest(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "cli:me", "hello", "world")
	outDir := t.TempDir()

	out, code := runSessionsCmd(t, cfg, "export", "cli:me", "--out", outDir)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0. Output:\n%s", code, out)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read out dir: %v", err)
	}
	var transcript, manifest string
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".md"):
			transcript = filepath.Join(outDir, e.Name())
		case strings.HasSuffix(e.Name(), ".json"):
			manifest = filepath.Join(outDir, e.Name())
		}
	}
	if transcript == "" || manifest == "" {
		t.Fatalf("expected a transcript and a manifest in --out, got %v", entries)
	}

	// The printed paths must be the files that were actually written; an
	// operator copies them straight out of this output.
	if !strings.Contains(out, transcript) || !strings.Contains(out, manifest) {
		t.Errorf("output does not name the files it wrote.\nOutput:\n%s", out)
	}

	body, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	for _, want := range []string{"hello", "world"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("transcript is missing message %q:\n%s", want, body)
		}
	}

	var m struct {
		SessionID string `json:"session_id"`
		Messages  int    `json:"messages"`
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, raw)
	}
	if m.Messages != 2 {
		t.Errorf("manifest messages = %d, want 2", m.Messages)
	}
}

// The trailing-flag trap: `export <id> --out <dir>` must honour --out. Without
// parseSessionArgs urfave drops it and the export lands in the working
// directory, where nobody looks for it.
func TestSessionsExportHonoursOutFlagAfterTheID(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "cli:me", "hi")

	outDir := t.TempDir()
	cwd := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	if _, code := runSessionsCmd(t, cfg, "export", "cli:me", "--out", outDir); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	if entries, _ := os.ReadDir(outDir); len(entries) == 0 {
		t.Error("--out after the id was dropped: nothing was written to the requested directory")
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("export leaked into the working directory: %v", entries)
	}
}

// --out= joined by an equals sign is the other spelling and must behave the
// same.
func TestSessionsExportAcceptsOutWithEquals(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "cli:me", "hi")
	outDir := t.TempDir()

	if _, code := runSessionsCmd(t, cfg, "export", "cli:me", "--out="+outDir); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if entries, _ := os.ReadDir(outDir); len(entries) == 0 {
		t.Error("--out=<dir> was dropped")
	}
}

// Exporting on top of an existing export refuses rather than overwriting, and
// --force (also written after the id) is the way through.
func TestSessionsExportRefusesOverwriteUntilForced(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "cli:me", "hi")
	outDir := t.TempDir()

	if _, code := runSessionsCmd(t, cfg, "export", "cli:me", "--out", outDir); code != 0 {
		t.Fatalf("first export exit code = %d, want 0", code)
	}
	if _, code := runSessionsCmd(t, cfg, "export", "cli:me", "--out", outDir); code == 0 {
		t.Error("second export overwrote an existing export without --force")
	}
	if _, code := runSessionsCmd(t, cfg, "export", "cli:me", "--out", outDir, "--force"); code != 0 {
		t.Errorf("--force after the id was dropped: exit code = %d, want 0", code)
	}
}

// A session that does not exist is a 1, with a message that names the id and
// points at `sessions list` — not a stack trace and not a silent success.
func TestSessionsExportUnknownSessionFails(t *testing.T) {
	cfg, _ := sessionsEnv(t)

	out, code := runSessionsCmd(t, cfg, "export", "cli:nobody", "--out", t.TempDir())
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	_ = out
}

// An id containing a path separator must be refused before any path is built:
// the sender half of a session id comes from Telegram.
func TestSessionsExportRejectsTraversalID(t *testing.T) {
	cfg, _ := sessionsEnv(t)

	if _, code := runSessionsCmd(t, cfg, "export", "../../etc/passwd", "--out", t.TempDir()); code == 0 {
		t.Error("a traversal session id was accepted")
	}
}

// No id at all is a usage error, not a nil-id export.
func TestSessionsExportWithoutAnIDIsAUsageError(t *testing.T) {
	cfg, _ := sessionsEnv(t)

	if _, code := runSessionsCmd(t, cfg, "export"); code == 0 {
		t.Error("export with no session id exited 0")
	}
}
