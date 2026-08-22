package main

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/bigknoxy/joshbot/internal/tools"
)

// The subagent tool adapter forwards to the registry and converts the result
// types; an unknown tool surfaces as an error result, never a panic, so a
// subagent naming a tool it does not have gets a readable refusal.
func TestToolExecutorAdapterForwardsAndConverts(t *testing.T) {
	a := &toolExecutorAdapter{registry: tools.NewRegistry()}
	if got := a.GetSchemas(); len(got) != 0 {
		t.Errorf("empty registry advertised %d tools", len(got))
	}
	res, async := a.ExecuteWithContext(context.Background(), "no_such_tool", map[string]any{}, "cli", "", nil)
	if async || res.Error == nil {
		t.Errorf("unknown tool: async=%v err=%v, want a synchronous error result", async, res.Error)
	}
}

// Declining the cron startup prompt (the default) installs nothing and
// returns nil. stdin is a pipe carrying the answer, PATH is emptied so a
// wrong branch reaching for crontab fails loudly.
func TestPromptCronStartupFallbackDeclines(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only prompt")
	}
	t.Setenv("PATH", t.TempDir())
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("2\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })

	out := captureStdout(t, func() {
		if err := promptCronStartupFallback(); err != nil {
			t.Errorf("declining = %v, want nil", err)
		}
	})
	if out == "" {
		t.Error("the prompt text was not printed")
	}
}
