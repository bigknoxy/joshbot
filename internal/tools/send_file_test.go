package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// sendFileFixture returns a tool contained to a fresh workspace plus the
// sender that records what it was handed.
func sendFileFixture(t *testing.T) (*SendFileTool, *mockSender, string) {
	t.Helper()
	ws := t.TempDir()
	ms := &mockSender{}
	return NewSendFileTool(ms, FilesystemToolConfig{Workspace: ws, Restrict: true}), ms, ws
}

// pngBytes is a real PNG signature plus filler, so the sniffer sees an image
// regardless of the filename it is written under.
func pngBytes(n int) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...)
	return b
}

func writeWS(t *testing.T, ws, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(ws, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSendFile_ImageIsSentAsPhotoWithInlineBytes(t *testing.T) {
	tool, ms, ws := sendFileFixture(t)
	writeWS(t, ws, "chart.png", pngBytes(32))

	res := tool.Execute(context.Background(), map[string]any{"path": "chart.png", "caption": "here"})
	if res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if ms.fileCalls != 1 {
		t.Fatalf("sender called %d times, want 1", ms.fileCalls)
	}
	if ms.file.Kind != bus.AttachmentPhoto {
		t.Errorf("kind = %q, want photo", ms.file.Kind)
	}
	if ms.file.MIME != "image/png" {
		t.Errorf("mime = %q, want image/png", ms.file.MIME)
	}
	if int64(len(ms.file.Data)) != ms.file.Size {
		t.Errorf("carried %d bytes for a %d byte file", len(ms.file.Data), ms.file.Size)
	}
	if ms.fileCaption != "here" {
		t.Errorf("caption = %q, want here", ms.fileCaption)
	}
	if ms.file.Path == "" {
		t.Error("Path must always be set so a channel without attachment support can name the file")
	}
}

// The routing rule: content decides, never the extension.
func TestSendFile_TextNamedPNGIsADocument(t *testing.T) {
	tool, ms, ws := sendFileFixture(t)
	writeWS(t, ws, "notes.png", []byte("this is prose, not an image at all"))

	if res := tool.Execute(context.Background(), map[string]any{"path": "notes.png"}); res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if ms.file.Kind != bus.AttachmentDocument {
		t.Errorf("kind = %q, want document — the bytes are text however the file is named", ms.file.Kind)
	}
	if strings.HasPrefix(ms.file.MIME, "image/") {
		t.Errorf("mime = %q, want a text type", ms.file.MIME)
	}
}

func TestSendFile_JPEGNamedDatIsAPhoto(t *testing.T) {
	tool, ms, ws := sendFileFixture(t)
	writeWS(t, ws, "blob.dat", append([]byte("\xff\xd8\xff"), make([]byte, 64)...))

	if res := tool.Execute(context.Background(), map[string]any{"path": "blob.dat"}); res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if ms.file.Kind != bus.AttachmentPhoto {
		t.Errorf("kind = %q, want photo — the bytes are a JPEG", ms.file.Kind)
	}
}

func TestSendFile_CaptionlessSendSucceeds(t *testing.T) {
	tool, ms, ws := sendFileFixture(t)
	writeWS(t, ws, "chart.png", pngBytes(16))

	if res := tool.Execute(context.Background(), map[string]any{"path": "chart.png"}); res.Error != nil {
		t.Fatalf("a file with no caption must send: %v", res.Error)
	}
	if ms.fileCaption != "" {
		t.Errorf("caption = %q, want empty", ms.fileCaption)
	}
}

// neutralDir returns a temp directory whose path contains none of the words
// isContainmentError looks for — a t.TempDir() path embeds the test's own name,
// which would let a plain "no such file" masquerade as a containment refusal.
func neutralDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "jb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestSendFile_PathOutsideWorkspaceSendsNothing(t *testing.T) {
	ms := &mockSender{}
	tool := NewSendFileTool(ms, FilesystemToolConfig{Workspace: neutralDir(t), Restrict: true})
	outside := filepath.Join(neutralDir(t), "secret.txt")
	if err := os.WriteFile(outside, []byte("keys"), 0600); err != nil {
		t.Fatal(err)
	}

	res := tool.Execute(context.Background(), map[string]any{"path": outside})
	if res.Error == nil {
		t.Fatal("want an error for a path outside the workspace")
	}
	// The refusal must be containment, not an incidental "no such file": a
	// path check that merely fails to find the file would admit any path that
	// happens to exist under the workspace prefix.
	if !isContainmentError(res.Error) {
		t.Errorf("error %q does not report a containment failure", res.Error)
	}
	if ms.fileCalls != 0 {
		t.Errorf("sender called %d times, want 0 — nothing may be published on a containment failure", ms.fileCalls)
	}
}

// An intermediate component swapped for a symlink is the case O_NOFOLLOW alone
// does not cover; the openat walk must refuse it.
func TestSendFile_IntermediateSymlinkEscapeSendsNothing(t *testing.T) {
	ws := neutralDir(t)
	ms := &mockSender{}
	tool := NewSendFileTool(ms, FilesystemToolConfig{Workspace: ws, Restrict: true})
	outside := neutralDir(t)
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("keys"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "sub")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res := tool.Execute(context.Background(), map[string]any{"path": "sub/secret.txt"})
	if res.Error == nil {
		t.Fatal("want an error for an escape through an intermediate symlink")
	}
	if !isContainmentError(res.Error) {
		t.Errorf("error %q does not report a containment failure", res.Error)
	}
	if ms.fileCalls != 0 {
		t.Errorf("sender called %d times, want 0", ms.fileCalls)
	}
}

// isContainmentError matches either half of the two-layer defence: the
// lexical/symlink check in resolvePath, or the openat walk that backs it.
func isContainmentError(err error) bool {
	msg := strings.ToLower(err.Error())
	// "workspace" is deliberately not in this list: a t.TempDir() path can
	// contain the test's own name, which would match a plain "no such file".
	for _, want := range []string{"outside", "escape", "not allowed", "denied", "access denied"} {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

func TestSendFile_OversizeNamesTheLimitAndSendsNothing(t *testing.T) {
	tool, ms, ws := sendFileFixture(t)
	tool.SetLimits(bus.AttachmentLimits{PhotoMaxBytes: 4, DocumentMaxBytes: 8, InlineMaxBytes: 4})
	writeWS(t, ws, "big.bin", make([]byte, 64))

	res := tool.Execute(context.Background(), map[string]any{"path": "big.bin"})
	if res.Error == nil {
		t.Fatal("want an error for an over-limit file")
	}
	if !strings.Contains(res.Error.Error(), humanBytes(8)) {
		t.Errorf("error %q must name the limit it enforced", res.Error)
	}
	if ms.fileCalls != 0 {
		t.Errorf("sender called %d times, want 0", ms.fileCalls)
	}
}

// An image too big to be a photo is still deliverable as a document. Refusing
// it outright would be a worse answer than a downloadable file.
func TestSendFile_ImageOverPhotoLimitBecomesADocument(t *testing.T) {
	tool, ms, ws := sendFileFixture(t)
	tool.SetLimits(bus.AttachmentLimits{PhotoMaxBytes: 16, DocumentMaxBytes: 1 << 20, InlineMaxBytes: 1 << 20})
	writeWS(t, ws, "huge.png", pngBytes(512))

	if res := tool.Execute(context.Background(), map[string]any{"path": "huge.png"}); res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if ms.file.Kind != bus.AttachmentDocument {
		t.Errorf("kind = %q, want document", ms.file.Kind)
	}
	if ms.file.MIME != "image/png" {
		t.Errorf("mime = %q — the type is still the sniffed one, only the routing changed", ms.file.MIME)
	}
}

// Above the inline threshold the bytes stay on disk and the channel streams
// from Path; copying a 50 MiB file through the bus is what this avoids.
func TestSendFile_LargeFileCarriesPathNotBytes(t *testing.T) {
	tool, ms, ws := sendFileFixture(t)
	tool.SetLimits(bus.AttachmentLimits{PhotoMaxBytes: 1 << 20, DocumentMaxBytes: 1 << 20, InlineMaxBytes: 8})
	writeWS(t, ws, "big.bin", make([]byte, 64))

	if res := tool.Execute(context.Background(), map[string]any{"path": "big.bin"}); res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if ms.file.Data != nil {
		t.Errorf("carried %d bytes inline, want none above the threshold", len(ms.file.Data))
	}
	if ms.file.Path == "" {
		t.Error("Path must be set when the bytes are not carried, or nothing can send the file")
	}
}

func TestSendFile_DirectoryAndEmptyFileAreRefused(t *testing.T) {
	tool, ms, ws := sendFileFixture(t)
	if err := os.Mkdir(filepath.Join(ws, "dir"), 0755); err != nil {
		t.Fatal(err)
	}
	writeWS(t, ws, "empty.txt", nil)

	for _, p := range []string{"dir", "empty.txt"} {
		if res := tool.Execute(context.Background(), map[string]any{"path": p}); res.Error == nil {
			t.Errorf("%s: want an error", p)
		}
	}
	if ms.fileCalls != 0 {
		t.Errorf("sender called %d times, want 0", ms.fileCalls)
	}
}

func TestSendFile_SenderErrorPropagates(t *testing.T) {
	tool, ms, ws := sendFileFixture(t)
	writeWS(t, ws, "chart.png", pngBytes(16))
	ms.sendFileFn = func(ctx context.Context, channel string, att Attachment, caption string) error {
		return errors.New("queue full")
	}

	res := tool.Execute(context.Background(), map[string]any{"path": "chart.png"})
	if res.Error == nil || !strings.Contains(res.Error.Error(), "queue full") {
		t.Fatalf("error = %v, want the sender's failure surfaced", res.Error)
	}
}

func TestSendFile_WorkspaceFromContextWins(t *testing.T) {
	tool, ms, _ := sendFileFixture(t)
	other := t.TempDir()
	writeWS(t, other, "chart.png", pngBytes(16))

	ctx := context.WithValue(context.Background(), ContextKeyWorkspace, other)
	if res := tool.Execute(ctx, map[string]any{"path": "chart.png"}); res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if ms.file.Filename != "chart.png" {
		t.Errorf("filename = %q", ms.file.Filename)
	}
}

func TestSendFile_NoSenderIsAnError(t *testing.T) {
	tool := NewSendFileTool(nil, FilesystemToolConfig{Workspace: t.TempDir(), Restrict: true})
	if res := tool.Execute(context.Background(), map[string]any{"path": "x"}); res.Error == nil {
		t.Fatal("want an error with no sender wired")
	}
}

func TestSendFile_LimitsAccessorFillsZeroFields(t *testing.T) {
	tool, _, _ := sendFileFixture(t)
	tool.SetLimits(bus.AttachmentLimits{PhotoMaxBytes: 7})
	got := tool.Limits()
	if got.PhotoMaxBytes != 7 {
		t.Errorf("PhotoMaxBytes = %d, want the override", got.PhotoMaxBytes)
	}
	if got.DocumentMaxBytes != bus.DefaultDocumentMaxBytes || got.InlineMaxBytes != bus.DefaultInlineMaxBytes {
		t.Errorf("a zero field must fall back to the default, got %+v", got)
	}
}

func TestSendFile_IsRegisteredWhenASenderExists(t *testing.T) {
	reg := RegistryWithDefaults(t.TempDir(), true, 0, 0, &mockSender{}, nil, nil, nil)
	if _, ok := reg.Get("send_file"); !ok {
		t.Fatal("send_file is not registered when a message sender exists")
	}

	bare := RegistryWithDefaults(t.TempDir(), true, 0, 0, nil, nil, nil, nil)
	if _, ok := bare.Get("send_file"); ok {
		t.Error("send_file must not be offered with no sender to deliver through")
	}
}
