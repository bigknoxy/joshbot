package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
)

// TestResolveListen pins the precedence chain --listen > api.listen > default.
//
// The flag case is the one that matters. This endpoint reaches the shell and
// filesystem tools, so an operator who narrows the bind on the command line
// while config.json still says 0.0.0.0 is making a security decision; a
// precedence bug there publishes those tools to the local network and reports
// the address the operator asked for in the banner either way.
func TestResolveListen(t *testing.T) {
	cases := []struct {
		name       string
		flag, conf string
		want       string
	}{
		{"flag overrides config", "127.0.0.1:1", "0.0.0.0:2", "127.0.0.1:1"},
		{"config used when no flag", "", "0.0.0.0:2", "0.0.0.0:2"},
		{"default when neither", "", "", config.DefaultAPIListen},
		{"blank flag is unset, not an override", "   ", "0.0.0.0:2", "0.0.0.0:2"},
		{"blank config falls through to the default", "", "  ", config.DefaultAPIListen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveListen(tc.flag, tc.conf); got != tc.want {
				t.Fatalf("resolveListen(%q, %q) = %q, want %q", tc.flag, tc.conf, got, tc.want)
			}
		})
	}
}

// A broken `stt` block must stop `joshbot serve` at startup, naming the key.
//
// The alternative is what the code originally risked: an unusable stt config is
// noticed nowhere, `transcriber` stays nil, and POST /v1/audio/transcriptions
// answers 501 "Transcription is not configured" to an operator who configured
// it. That reads as a missing feature, not a typo, so nobody goes looking at
// the key they got wrong.
func TestRunServeRefusesABrokenSTTBlock(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {APIKey: "k", Model: "test-model", Enabled: true},
	}
	cfg.API.APIKeys = []string{"secret"}
	// A provider that is not configured at all: buildTranscriber's own tests
	// cover every rejection reason, so this only needs one that is certain.
	cfg.STT = config.STTConfig{Provider: "nope"}

	path := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.String("config", path, "")
	fs.String("listen", "127.0.0.1:0", "")
	fs.String("profile", "", "")

	err = runServe(cli.NewContext(cli.NewApp(), fs, nil))
	if err == nil {
		t.Fatal("runServe returned nil for an unusable stt block; it must refuse to start")
	}
	if !strings.Contains(err.Error(), "speech-to-text config") {
		t.Fatalf("error = %q, want it to name the stt config", err)
	}
}
