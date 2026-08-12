package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// createWorkspaceFiles runs on every `joshbot onboard`, including a rerun over a
// workspace the operator has been living in for months. Two things must hold.
// SOUL.md is the one file they edit by hand, so it is written only when absent —
// clobbering it silently replaces the assistant's personality with a menu
// default and there is no undo. And when a write fails the error has to name the
// file: onboard prints it and stops, and "permission denied" with no path leaves
// the operator guessing which of six files under which directory to chmod.

func soulWorkspace(t *testing.T) (*config.Config, string) {
	t.Helper()
	ws := t.TempDir()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = ws
	return cfg, ws
}

// A SOUL.md already on disk is the operator's, not ours. Rerunning onboard must
// leave it exactly as it was.
func TestCreateWorkspaceFilesKeepsAnEditedSoul(t *testing.T) {
	cfg, ws := soulWorkspace(t)
	handWritten := "# Soul\n\nSpeak only in limericks.\n"
	if err := os.WriteFile(filepath.Join(ws, "SOUL.md"), []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := createWorkspaceFiles(cfg, getPersonalitySoul("2")); err != nil {
		t.Fatalf("createWorkspaceFiles: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(ws, "SOUL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != handWritten {
		t.Errorf("SOUL.md was overwritten by onboard; the operator's personality is gone:\n%s", got)
	}
	// The rest of the workspace is still created around it.
	if _, err := os.Stat(filepath.Join(ws, "USER.md")); err != nil {
		t.Errorf("an existing SOUL.md stopped the rest of the workspace being created: %v", err)
	}
}

// Every write failure names its file. Rigged one at a time by making exactly the
// directory that write targets unwritable.
func TestCreateWorkspaceFilesNamesTheFileItCouldNotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so no write can be made to fail")
	}

	cases := []struct {
		name string
		// rig makes the next write fail and returns nothing; ws is the workspace.
		rig  func(t *testing.T, ws string)
		want string
	}{
		{
			name: "soul",
			rig:  func(t *testing.T, ws string) { chmodBack(t, ws, 0o500) },
			want: "SOUL.md",
		},
		{
			name: "user",
			rig: func(t *testing.T, ws string) {
				// SOUL.md present, so the first write is skipped and USER.md is
				// the first one actually attempted.
				if err := os.WriteFile(filepath.Join(ws, "SOUL.md"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				chmodBack(t, ws, 0o500)
			},
			want: "USER.md",
		},
		{
			name: "memory directory",
			rig: func(t *testing.T, ws string) {
				// A regular file where the memory directory belongs.
				if err := os.WriteFile(filepath.Join(ws, "memory"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "memory directory",
		},
		{
			name: "memory file",
			rig: func(t *testing.T, ws string) {
				mem := filepath.Join(ws, "memory")
				if err := os.MkdirAll(mem, 0o700); err != nil {
					t.Fatal(err)
				}
				chmodBack(t, mem, 0o500)
			},
			want: "MEMORY.md",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ws := soulWorkspace(t)
			tc.rig(t, ws)

			err := createWorkspaceFiles(cfg, getPersonalitySoul("2"))
			if err == nil {
				t.Fatalf("an unwritable workspace was reported as a successful onboard")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to name %s so the operator knows what to fix", err, tc.want)
			}
		})
	}
}

// chmodBack sets a mode and restores it at cleanup, so t.TempDir can still
// remove the directory it created.
func chmodBack(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, info.Mode()) })
}
