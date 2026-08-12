package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/mcp"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// The MCP trust store is what decides which server-advertised tools may reach
// the model. If it cannot be read, the only safe reading is "nothing is
// approved" — carrying on with a fresh empty store would be indistinguishable
// from an operator who had approved nothing, but carrying on with servers
// *running* would still spawn every configured process. registerMCPServers
// must stop before that, and say why.

func TestRegisterMCPServersFailsClosedOnAnUnreadableTrustStore(t *testing.T) {
	withTempHome(t)

	// Malformed, not missing: a missing store is a legitimate "nothing
	// approved yet" and must not be an error.
	path := mcp.DefaultTrustStorePath(config.DefaultHome)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := tools.NewRegistry()
	before := len(reg.List())

	cfg := config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		// Would spawn a process if the store failure were ignored.
		"local": {Command: "/bin/echo", Enabled: true},
	}}

	out := captureLogOutput(t, func() {
		registerMCPServers(context.Background(), reg, cfg)
	})
	t.Cleanup(closeMCPServers)

	if got := len(reg.List()); got != before {
		t.Errorf("registry grew from %d to %d tools; an unreadable approval store must register nothing", before, got)
	}
	mcpMu.Lock()
	mgr := mcpManager
	mcpMu.Unlock()
	if mgr != nil {
		t.Error("servers were started despite the approval store being unreadable")
	}
	if !strings.Contains(out, "approval store") {
		t.Errorf("the operator was not told why MCP is off:\n%s", out)
	}
}

// A missing store is the normal first-run state, not a failure: the servers
// still start so their manifests can be enumerated and offered for approval.
// Treating "no file" as an error would make `joshbot mcp trust` impossible to
// reach, since it needs the advertised manifest.
func TestRegisterMCPServersTreatsAMissingTrustStoreAsNothingApproved(t *testing.T) {
	withTempHome(t)

	reg := tools.NewRegistry()
	before := len(reg.List())

	cfg := config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		// Not an MCP server: it exits immediately, so the connect fails and
		// is swallowed. What matters is that we got past the store load.
		"local": {Command: "/bin/echo", Enabled: true},
	}}

	registerMCPServers(context.Background(), reg, cfg)
	t.Cleanup(closeMCPServers)

	mcpMu.Lock()
	mgr := mcpManager
	mcpMu.Unlock()
	if mgr == nil {
		t.Fatal("a missing trust store stopped MCP entirely; first-run approval would be unreachable")
	}
	if got := len(reg.List()); got != before {
		t.Errorf("an unapproved server contributed %d tools", got-before)
	}
}
