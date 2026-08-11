package config

import (
	"encoding/json"
	"testing"
)

// TestMCPConfigJSONRoundTrip pins the wire format of the MCP config so a rename
// of a struct tag (which would silently break existing config.json files) fails
// this test.
func TestMCPConfigJSONRoundTrip(t *testing.T) {
	raw := `{
		"mcp": {
			"servers": {
				"filesystem": {
					"command": "npx",
					"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
					"env": {"FOO": "bar"},
					"enabled": true
				}
			}
		}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	srv, ok := cfg.MCP.Servers["filesystem"]
	if !ok {
		t.Fatal("filesystem server not parsed")
	}
	if srv.Command != "npx" {
		t.Errorf("command = %q, want npx", srv.Command)
	}
	if len(srv.Args) != 3 || srv.Args[0] != "-y" {
		t.Errorf("args = %v", srv.Args)
	}
	if srv.Env["FOO"] != "bar" {
		t.Errorf("env = %v", srv.Env)
	}
	if !srv.Enabled {
		t.Error("enabled should be true")
	}

	// Round-trip back out and confirm keys survive.
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"mcp"`, `"servers"`, `"command"`, `"args"`, `"enabled"`} {
		if !contains(string(out), want) {
			t.Errorf("marshalled config missing %s: %s", want, out)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
