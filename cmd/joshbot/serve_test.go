package main

import (
	"testing"

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
