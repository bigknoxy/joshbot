package main

import (
	"path/filepath"
	"testing"
)

// `go run` builds into the go-build cache; nothing else about a path implies
// it.
//
// Three call sites — update, uninstall and detectRunningContext — also treated
// any path containing "/tmp/" as `go run`. That is not a property of `go run`:
// it made a joshbot installed anywhere under /tmp permanently unable to update
// or uninstall itself, and reported the cause as `go run`, which was neither
// true nor actionable.
func TestRunningFromGoRunMatchesOnlyTheBuildCache(t *testing.T) {
	goRun := []string{
		"/tmp/go-build3456789/b001/exe/main",
		"/home/user/.cache/go-build/ab/abcdef-d/main",
		filepath.Join("/var/folders/xy/T/go-build123/b001/exe", "joshbot"),
	}
	for _, p := range goRun {
		if !runningFromGoRun(p) {
			t.Errorf("%q is a go run binary but was not detected", p)
		}
	}

	installed := []string{
		"/home/user/.local/bin/joshbot",
		"/usr/local/bin/joshbot",
		"/tmp/sandbox/bin/joshbot",   // the case that was broken
		"/home/user/tmp/bin/joshbot", // "/tmp/" appears mid-path
		"/opt/joshbot/bin/joshbot",
	}
	for _, p := range installed {
		if runningFromGoRun(p) {
			t.Errorf("%q is an installed binary but was treated as go run", p)
		}
	}
}
