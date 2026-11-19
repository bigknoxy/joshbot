package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These tests cover the path the spill mechanism actually takes in a run:
// the registry is configured with a threshold, a tool produces more output
// than the threshold, and the model ends up with a handle it can follow.
//
// Before #431 the spill never fired in a real run for two independent
// reasons, and both had to be fixed for this to pass:
//
//   - The spill configuration lived on the registry and was read from the
//     tool execution context, which nothing ever attached it to.
//   - The built-in tools truncated their own output first, at 8000
//     characters for a file read and 4000 for a command, both below the
//     spill threshold - so the registry never saw anything to spill.

var spillHandleRe = regexp.MustCompile(`spill_[0-9a-f]{8} \((\d+) bytes\) at (\S+)`)

func spillTestRegistry(t *testing.T, workspace string, threshold int) *Registry {
	t.Helper()
	return RegistryWithDefaults(workspace, true, 5, 5, nil, nil, nil, nil,
		WithSpillConfig(SpillConfig{
			Threshold: threshold,
			MaxSize:   1 << 20,
			MaxAge:    0,
		}),
	)
}

// writeBigFile writes a file whose first read is well over the spill
// threshold: read_file returns at most 100 lines, so the lines have to be
// long rather than numerous.
func writeBigFile(t *testing.T, workspace, name string) string {
	t.Helper()
	path := filepath.Join(workspace, name)
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", 200))
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write big file: %v", err)
	}
	return path
}

func spillHandle(t *testing.T, output string) (size int, path string) {
	t.Helper()
	m := spillHandleRe.FindStringSubmatch(output)
	if m == nil {
		t.Fatalf("expected a spill handle, got: %q", output)
	}
	n := 0
	for _, c := range m[1] {
		n = n*10 + int(c-'0')
	}
	return n, m[2]
}

func TestSpillReachableAReadOverTheThresholdBecomesAHandle(t *testing.T) {
	workspace := t.TempDir()
	writeBigFile(t, workspace, "big.txt")

	reg := spillTestRegistry(t, workspace, 8192)
	result, _ := reg.ExecuteWithContext(context.Background(), "read_file",
		map[string]any{"operation": "read_file", "path": "big.txt"}, "cli", "user", nil)
	if result.Error != nil {
		t.Fatalf("read_file: %v", result.Error)
	}

	size, path := spillHandle(t, result.Output)
	if size <= 8192 {
		t.Fatalf("the handle reports %d bytes, want more than the threshold", size)
	}
	// The path in the handle has to be the real path, or the model cannot
	// follow it.
	if !filepath.IsAbs(path) {
		t.Fatalf("expected an absolute path in the handle, got: %q", path)
	}
	if !strings.HasPrefix(path, workspace) {
		t.Fatalf("spill escaped the workspace: %q not under %q", path, workspace)
	}

	// The handle has to say how to read it. A live run showed what a bare
	// handle costs: the model tried to glob for the original file, found
	// nothing, and ran `find /` - thirteen minutes of the user's time.
	if !strings.Contains(result.Output, "read_file(path=") {
		t.Fatalf("the handle does not tell the model how to read it: %q", result.Output)
	}
	if !strings.Contains(result.Output, path) {
		t.Fatalf("the instruction does not name the path: %q", result.Output)
	}

	// The head and the tail go with the handle: the head is usually the
	// answer to the question that caused the read.
	if !strings.Contains(result.Output, "[HEAD]") || !strings.Contains(result.Output, "[TAIL]") {
		t.Fatalf("the handle has no head and tail: %q", result.Output)
	}
	if !strings.Contains(result.Output, strings.Repeat("x", 200)) {
		t.Fatalf("the head does not carry the beginning of the result")
	}
}

func TestSpillReachableTheFullContentIsOnDiskAndRecoverable(t *testing.T) {
	workspace := t.TempDir()
	writeBigFile(t, workspace, "big.txt")

	reg := spillTestRegistry(t, workspace, 8192)
	result, _ := reg.ExecuteWithContext(context.Background(), "read_file",
		map[string]any{"operation": "read_file", "path": "big.txt"}, "cli", "user", nil)
	if result.Error != nil {
		t.Fatalf("read_file: %v", result.Error)
	}

	_, path := spillHandle(t, result.Output)
	spilled, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the spill file: %v", err)
	}
	if len(spilled) <= 8192 {
		t.Fatalf("the spill file holds %d bytes, want the whole result", len(spilled))
	}
	if !strings.HasPrefix(string(spilled), "File: ") {
		t.Fatalf("the spill file does not hold the tool output: %q", string(spilled[:40]))
	}
	if !strings.Contains(string(spilled), strings.Repeat("x", 200)) {
		t.Fatalf("the spill file does not contain the original text")
	}

	// The file carries tool output, which can carry credentials the tool
	// printed. Owner-only, like the rest of joshbot's private state.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the spill file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("spill file mode is %o, want 600", perm)
	}

	// The model follows the reference with the tool it already has. The
	// read-back goes through a registry without a spill, because a spill
	// of a spill is a handle to a handle - the model would have to follow
	// two of them to get back to the text.
	plain := RegistryWithDefaults(workspace, true, 5, 5, nil, nil, nil, nil)
	back, _ := plain.ExecuteWithContext(context.Background(), "read_file",
		map[string]any{"operation": "read_file", "path": path}, "cli", "user", nil)
	if back.Error != nil {
		t.Fatalf("read back: %v", back.Error)
	}
	if !strings.Contains(back.Output, strings.Repeat("x", 200)) {
		t.Fatalf("the recovered content does not contain the original text")
	}
}

func TestSpillReachableTheSpillStaysInsideTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	writeBigFile(t, workspace, "big.txt")

	reg := spillTestRegistry(t, workspace, 8192)
	result, _ := reg.ExecuteWithContext(context.Background(), "read_file",
		map[string]any{"operation": "read_file", "path": "big.txt"}, "cli", "user", nil)
	if result.Error != nil {
		t.Fatalf("read_file: %v", result.Error)
	}

	_, path := spillHandle(t, result.Output)
	rel, err := filepath.Rel(workspace, path)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	if strings.HasPrefix(rel, "..") {
		t.Fatalf("spill escaped the workspace: %q", path)
	}
	if !strings.Contains(rel, "spill") {
		t.Fatalf("expected the spill under a spill directory, got: %q", rel)
	}
}

func TestSpillReachableWithoutAThresholdTheOutputIsStillTruncated(t *testing.T) {
	workspace := t.TempDir()
	writeBigFile(t, workspace, "big.txt")

	// No spill configuration: the old behaviour has to survive it.
	reg := RegistryWithDefaults(workspace, true, 5, 5, nil, nil, nil, nil)
	result, _ := reg.ExecuteWithContext(context.Background(), "read_file",
		map[string]any{"operation": "read_file", "path": "big.txt"}, "cli", "user", nil)
	if result.Error != nil {
		t.Fatalf("read_file: %v", result.Error)
	}
	if strings.Contains(result.Output, "spill_") {
		t.Fatalf("spilled without a threshold: %q", result.Output)
	}
	if !strings.Contains(result.Output, "truncated") {
		t.Fatalf("expected the output to be truncated, got: %q", result.Output)
	}
	if len(result.Output) > 9000 {
		t.Fatalf("expected the output to be cut to the tool limit, got %d bytes", len(result.Output))
	}
}

func TestSpillReachableACommandOverTheThresholdBecomesAHandle(t *testing.T) {
	workspace := t.TempDir()
	reg := spillTestRegistry(t, workspace, 8192)

	result, _ := reg.ExecuteWithContext(context.Background(), "shell",
		map[string]any{"command": "awk 'BEGIN{s=\"\";for(i=0;i<20000;i++)s=s \"y\";print s}'"}, "cli", "user", nil)
	if result.Error != nil {
		t.Fatalf("shell: %v", result.Error)
	}
	if !strings.Contains(result.Output, "spill_") {
		t.Fatalf("expected a spill handle, got %d bytes: %q", len(result.Output), result.Output)
	}
}
