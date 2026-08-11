package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// exportFixture writes a session file directly, so the test controls the bytes
// on disk rather than whatever Save happens to produce.
func exportFixture(t *testing.T, lines ...string) (*Manager, string, string) {
	t.Helper()
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	id := "cli:tester"
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	return mgr, id, path
}

func msgLine(t *testing.T, m Message) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	return string(b)
}

func ts(min int) time.Time {
	return time.Date(2026, 1, 2, 3, min, 0, 0, time.UTC)
}

// TestExportRedactsBeforeWriting is the security contract. An export is made to
// be attached to a bug report, so a credential or a username reaching the file
// is a leak that happens after the operator has stopped looking.
func TestExportRedactsBeforeWriting(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	homePath := filepath.Join(home, "projects", "secret-client")

	mgr, id, _ := exportFixture(t,
		msgLine(t, Message{Role: RoleUser, Content: "here is my key: sk-ant-api03-EXAMPLEcredential0123456789", Timestamp: ts(1)}),
		msgLine(t, Message{
			Role:      RoleAssistant,
			Content:   "working in " + homePath,
			Timestamp: ts(2),
			ToolCalls: []ToolCall{{
				ID:        "1",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"env"}`),
				Result:    "AUTHORIZATION: Bearer sk-ant-api03-EXAMPLEcredential0123456789\nHOME=" + home,
			}},
		}),
	)

	res, err := mgr.Export(context.Background(), id, t.TempDir(), false)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	for _, p := range []string{res.TranscriptPath, res.ManifestPath} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		body := string(data)
		if strings.Contains(body, "sk-ant-api03-EXAMPLEcredential0123456789") {
			t.Fatalf("a credential reached %s:\n%s", p, body)
		}
		if strings.Contains(body, home) {
			t.Fatalf("the host home directory reached %s:\n%s", p, body)
		}
	}

	// The redaction must not be achieved by dropping the content entirely —
	// an export with nothing in it is safe and useless.
	data, _ := os.ReadFile(res.TranscriptPath)
	if !strings.Contains(string(data), "projects/secret-client") {
		t.Fatalf("redaction removed the path instead of the username:\n%s", data)
	}
}

// TestExportIsDeterministic — the export exists to be compared and re-attached.
// A wall-clock stamp anywhere in it makes every run differ, so a reporter can
// never show the file was not edited between runs.
func TestExportIsDeterministic(t *testing.T) {
	mgr, id, _ := exportFixture(t,
		msgLine(t, Message{Role: RoleUser, Content: "hello", Timestamp: ts(1)}),
		msgLine(t, Message{Role: RoleAssistant, Content: "hi", Timestamp: ts(2)}),
	)

	first := t.TempDir()
	second := t.TempDir()
	a, err := mgr.Export(context.Background(), id, first, false)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	b, err := mgr.Export(context.Background(), id, second, false)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}

	for _, pair := range [][2]string{
		{a.TranscriptPath, b.TranscriptPath},
		{a.ManifestPath, b.ManifestPath},
	} {
		x, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatalf("read %s: %v", pair[0], err)
		}
		y, err := os.ReadFile(pair[1])
		if err != nil {
			t.Fatalf("read %s: %v", pair[1], err)
		}
		if string(x) != string(y) {
			t.Fatalf("two exports of an unchanged session differ:\n--- %s\n%s\n--- %s\n%s",
				pair[0], x, pair[1], y)
		}
	}
}

// TestExportManifestDigestMatchesTheSourceFile — the digest is the only thing
// tying the transcript to the file it claims to describe. Computed here
// independently so a bug that hashed the transcript instead is caught.
func TestExportManifestDigestMatchesTheSourceFile(t *testing.T) {
	mgr, id, srcPath := exportFixture(t,
		msgLine(t, Message{Role: RoleUser, Content: "hello", Timestamp: ts(1)}),
	)

	res, err := mgr.Export(context.Background(), id, t.TempDir(), false)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	sum := sha256.Sum256(raw)
	want := hex.EncodeToString(sum[:])

	var man ExportManifest
	data, err := os.ReadFile(res.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(data, &man); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, data)
	}
	if man.SHA256 != want {
		t.Fatalf("manifest sha256 = %q, want the source file's %q", man.SHA256, want)
	}
	if man.SourceBytes != int64(len(raw)) {
		t.Fatalf("manifest source_bytes = %d, want %d", man.SourceBytes, len(raw))
	}
}

// TestExportDoesNotTouchTheSession keeps the export inert. Load quarantines
// corrupt input, and an export that repaired the session would destroy the
// evidence it was run to capture.
func TestExportDoesNotTouchTheSession(t *testing.T) {
	mgr, id, srcPath := exportFixture(t,
		msgLine(t, Message{Role: RoleUser, Content: "hello", Timestamp: ts(1)}),
		"{ this line is not json",
		msgLine(t, Message{Role: RoleAssistant, Content: "hi", Timestamp: ts(2)}),
	)
	before, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	dir := filepath.Dir(srcPath)
	beforeEntries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	res, err := mgr.Export(context.Background(), id, t.TempDir(), false)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	after, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read source after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("the export modified the session file it was describing")
	}
	afterEntries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir after: %v", err)
	}
	if len(afterEntries) != len(beforeEntries) {
		t.Fatalf("the export created a sidecar in the sessions directory: %d entries, was %d",
			len(afterEntries), len(beforeEntries))
	}

	// The recoverable content still exports, and the damage is reported rather
	// than left to be inferred from a short transcript.
	if res.Manifest.Messages != 2 {
		t.Fatalf("recoverable messages = %d, want 2", res.Manifest.Messages)
	}
	if res.Manifest.CorruptLines != 1 {
		t.Fatalf("corrupt_lines = %d, want 1", res.Manifest.CorruptLines)
	}
	body, err := os.ReadFile(res.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !strings.Contains(string(body), "unreadable line") {
		t.Fatalf("the transcript must flag that it is incomplete:\n%s", body)
	}
}

// TestExportRefusesToOverwriteWithoutForce — an export names itself after the
// session, so running it twice in a directory is the ordinary case. Silently
// replacing a copy already attached to a report destroys evidence.
func TestExportRefusesToOverwriteWithoutForce(t *testing.T) {
	mgr, id, _ := exportFixture(t,
		msgLine(t, Message{Role: RoleUser, Content: "hello", Timestamp: ts(1)}),
	)
	dir := t.TempDir()
	res, err := mgr.Export(context.Background(), id, dir, false)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	if err := os.WriteFile(res.TranscriptPath, []byte("hand edited"), 0o600); err != nil {
		t.Fatalf("edit export: %v", err)
	}

	_, err = mgr.Export(context.Background(), id, dir, false)
	if err == nil {
		t.Fatal("a second export must refuse rather than overwrite")
	}
	if !strings.Contains(err.Error(), res.TranscriptPath) {
		t.Fatalf("the refusal must name the file, got %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("the refusal must name the way through, got %v", err)
	}
	body, err := os.ReadFile(res.TranscriptPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "hand edited" {
		t.Fatal("a refused export must write nothing at all")
	}

	if _, err := mgr.Export(context.Background(), id, dir, true); err != nil {
		t.Fatalf("--force must replace: %v", err)
	}
	body, _ = os.ReadFile(res.TranscriptPath)
	if string(body) == "hand edited" {
		t.Fatal("--force did not replace the file")
	}
}

// TestExportLeavesNoTempFilesOnFailure — writeFileAtomic writes through
// ".tmp-*" in the destination directory, and a failure that leaves one behind
// puts a partial transcript next to the real one, where it will be attached to
// a report by mistake.
func TestExportLeavesNoTempFilesOnFailure(t *testing.T) {
	mgr, id, _ := exportFixture(t,
		msgLine(t, Message{Role: RoleUser, Content: "hello", Timestamp: ts(1)}),
	)
	dir := t.TempDir()

	// The manifest path is a directory, so writing it fails at the rename
	// while the transcript has already been written.
	_, manifestPath := ExportPaths(dir, id)
	if err := os.Mkdir(manifestPath, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := mgr.Export(context.Background(), id, dir, false); err == nil {
		t.Fatal("an unwritable manifest path must fail the export")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("a temporary file survived a failed export: %s", e.Name())
		}
		if strings.HasSuffix(e.Name(), ".export.md") {
			t.Fatalf("a half-export survived: %s reads as a complete transcript", e.Name())
		}
	}
}

// TestExportRejectsAnUnsafeSessionID — the sender half of a session ID comes
// from Telegram, so it is attacker-influenced. A traversal must be refused
// before any path is built from it, on the output side as much as the input.
func TestExportRejectsAnUnsafeSessionID(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	out := t.TempDir()

	for _, id := range []string{"../escape", "tg:../../etc/passwd", "a/b", ""} {
		if _, err := mgr.Export(context.Background(), id, out, false); err == nil {
			t.Fatalf("session id %q must be refused", id)
		}
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused id wrote %d file(s)", len(entries))
	}
}

// TestExportOfAMissingSessionIsNotFound so the CLI can name the id rather than
// reporting a raw filesystem error.
func TestExportOfAMissingSessionIsNotFound(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, err = mgr.Export(context.Background(), "cli:ghost", t.TempDir(), false)
	if err == nil {
		t.Fatal("exporting a session that does not exist must fail")
	}
	if err != ErrSessionNotFound {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
}

// TestExportManifestTalliesTools — the tallies are why the manifest exists: a
// gap between calls and results is the signature of a turn that died mid-tool,
// which is exactly what a bug report needs to show.
func TestExportManifestTalliesTools(t *testing.T) {
	mgr, id, _ := exportFixture(t,
		msgLine(t, Message{Role: RoleUser, Content: "go", Timestamp: ts(1)}),
		msgLine(t, Message{Role: RoleAssistant, Timestamp: ts(2), ToolCalls: []ToolCall{
			{ID: "1", Name: "shell", Arguments: json.RawMessage(`{}`), Result: "ok"},
			{ID: "2", Name: "shell", Arguments: json.RawMessage(`{}`)},
			{ID: "3", Name: "filesystem", Arguments: json.RawMessage(`{}`), Result: "ok"},
		}}),
	)

	res, err := mgr.Export(context.Background(), id, t.TempDir(), false)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	got := map[string]ExportToolUsage{}
	for _, u := range res.Manifest.Tools {
		got[u.Name] = u
	}
	if got["shell"].Calls != 2 || got["shell"].Results != 1 {
		t.Fatalf("shell tally = %+v, want 2 calls and 1 result", got["shell"])
	}
	if got["filesystem"].Calls != 1 || got["filesystem"].Results != 1 {
		t.Fatalf("filesystem tally = %+v", got["filesystem"])
	}
	if res.Manifest.Roles[string(RoleUser)] != 1 {
		t.Fatalf("role tally = %v", res.Manifest.Roles)
	}
	// Sorted, so the manifest bytes do not depend on Go's map iteration order.
	if res.Manifest.Tools[0].Name != "filesystem" {
		t.Fatalf("tool tallies must be in a stable order, got %+v", res.Manifest.Tools)
	}
}
