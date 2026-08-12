package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `prune --older-than` without --force is the one destructive command that
// deletes a set the operator never named. Everything it does before the delete
// exists so they can see that set first, and none of it had coverage: the
// existing tests all pass --force and skip the prompt entirely.

// withConfirm substitutes the confirmation prompt and records what it was asked.
func withConfirm(t *testing.T, answer bool) *[]string {
	t.Helper()
	var asked []string
	prev := confirmDestructive
	confirmDestructive = func(_ io.Writer, action string) bool {
		asked = append(asked, action)
		return answer
	}
	t.Cleanup(func() { confirmDestructive = prev })
	return &asked
}

// seedAged writes a session and backdates its mtime, which is what
// ListInfo reports as UpdatedAt.
func seedAged(t *testing.T, dir, id string, age time.Duration) {
	t.Helper()
	seedCLISession(t, dir, id, "x")
	chtime(t, filepath.Join(dir, id+".jsonl"), time.Now().Add(-age))
}

// The prompt is the operator's only chance to see what is about to go. Naming
// the wrong set — or a count that disagrees with the ids — makes an informed
// "y" impossible, and there is no undo behind it.
func TestPruneOlderThanPromptsWithExactlyTheDoomedSessions(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedAged(t, dir, "ancient", 90*24*time.Hour)
	seedAged(t, dir, "stale", 45*24*time.Hour)
	seedAged(t, dir, "fresh", time.Hour)

	asked := withConfirm(t, false)

	out, code := runSessionsCmd(t, cfg, "prune", "--older-than", "30d")
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	if len(*asked) != 1 {
		t.Fatalf("confirmations = %d, want exactly 1: %v", len(*asked), *asked)
	}
	prompt := (*asked)[0]
	for _, want := range []string{"ancient", "stale", "2 session"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt %q does not mention %q", prompt, want)
		}
	}
	if strings.Contains(prompt, "fresh") {
		t.Errorf("the prompt threatens a session that is not old enough: %q", prompt)
	}
}

// A declined prompt must leave every session on disk. Deleting anyway is
// unrecoverable, and reporting a deletion that did not happen is almost as bad:
// the operator stops looking for sessions they think are gone.
func TestPruneOlderThanDeclinedDeletesNothing(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedAged(t, dir, "ancient", 90*24*time.Hour)
	seedAged(t, dir, "fresh", time.Hour)

	withConfirm(t, false)

	out, code := runSessionsCmd(t, cfg, "prune", "--older-than", "30d")
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	for _, id := range []string{"ancient", "fresh"} {
		if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); err != nil {
			t.Errorf("%s was deleted after the operator declined: %v", id, err)
		}
	}
	if !strings.Contains(out, "Aborted") {
		t.Errorf("a refusal was not reported:\n%s", out)
	}
	if strings.Contains(out, "Deleted") {
		t.Errorf("a refusal reported a deletion:\n%s", out)
	}
}

// And an accepted prompt must delete exactly the set it named. A confirm that
// then prunes a wider cutoff is the worst outcome of all — the operator agreed
// to something else.
func TestPruneOlderThanConfirmedDeletesOnlyWhatWasNamed(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedAged(t, dir, "ancient", 90*24*time.Hour)
	seedAged(t, dir, "fresh", time.Hour)

	withConfirm(t, true)

	out, code := runSessionsCmd(t, cfg, "prune", "--older-than", "30d")
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "ancient.jsonl")); !os.IsNotExist(err) {
		t.Errorf("the confirmed session survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.jsonl")); err != nil {
		t.Errorf("a session younger than the cutoff was pruned: %v", err)
	}
	if !strings.Contains(out, "ancient") {
		t.Errorf("the deletion was not reported:\n%s", out)
	}
}

// With nothing old enough there is nothing to agree to. Prompting anyway trains
// operators to type y at a prune prompt without reading it.
func TestPruneOlderThanDoesNotPromptWhenNothingMatches(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedAged(t, dir, "fresh", time.Hour)

	asked := withConfirm(t, true)

	out, code := runSessionsCmd(t, cfg, "prune", "--older-than", "30d")
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	if len(*asked) != 0 {
		t.Errorf("prompted with nothing to delete: %v", *asked)
	}
	if !strings.Contains(out, "No sessions older than") {
		t.Errorf("the empty result was not reported:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.jsonl")); err != nil {
		t.Errorf("the only session was deleted: %v", err)
	}
}
