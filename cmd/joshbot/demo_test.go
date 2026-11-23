package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

// The demo command is the front door for someone who has not configured
// anything yet, so its behaviour is pinned: it must run, print the
// transcript, and leave a greeting on disk.
func TestRunDemo_PrintsTheTranscriptAndWritesTheFile(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	workspace := filepath.Join(home, "ws")

	out := captureStdout(t, func() {
		app := &cli.App{Commands: []*cli.Command{demoCommand()}}
		if err := app.Run([]string{"joshbot", "demo", "--workspace", workspace}); err != nil {
			t.Fatalf("demo failed: %v", err)
		}
	})

	data, err := os.ReadFile(filepath.Join(workspace, "hello.txt"))
	if err != nil {
		t.Fatalf("the demo did not write hello.txt: %v", err)
	}
	if !strings.Contains(string(data), "Hello from joshbot!") {
		t.Errorf("hello.txt does not hold the greeting: %q", data)
	}

	for _, want := range []string{
		"no API key, no network",
		"tool call",
		"Try it for real",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the demo transcript does not mention %q:\n%s", want, out)
		}
	}
}

// A workspace that does not exist yet is created, not an error: the demo is
// someone's first command.
func TestRunDemo_CreatesTheWorkspace(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	workspace := filepath.Join(home, "not-created-yet")

	captureStdout(t, func() {
		app := &cli.App{Commands: []*cli.Command{demoCommand()}}
		if err := app.Run([]string{"joshbot", "demo", "--workspace", workspace}); err != nil {
			t.Fatalf("demo failed: %v", err)
		}
	})

	if st, err := os.Stat(workspace); err != nil || !st.IsDir() {
		t.Errorf("the demo did not create the workspace: %v", err)
	}
}

// The default workspace is a temporary directory that is cleaned up, not a
// directory the demo litters into the user's home.
func TestRunDemo_DefaultWorkspaceIsTemporary(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	out := captureStdout(t, func() {
		app := &cli.App{Commands: []*cli.Command{demoCommand()}}
		if err := app.Run([]string{"joshbot", "demo"}); err != nil {
			t.Fatalf("demo failed: %v", err)
		}
	})

	if !strings.Contains(out, "joshbot-demo-") {
		t.Errorf("the transcript should name the temporary workspace:\n%s", out)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read home: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "joshbot-demo-") {
			t.Errorf("the demo left %s behind in the home", e.Name())
		}
	}
}
