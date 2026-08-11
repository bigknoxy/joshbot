package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/output"
)

// fakeMCPServerScript writes a shell script that speaks just enough
// newline-delimited JSON-RPC to answer initialize and tools/list, advertising a
// single "echo" tool with the given description. It is the smallest thing that
// makes the CLI's connect-and-read-the-manifest path real: the manifest is what
// is being approved, so a test that stubs it out proves nothing about the gate.
func fakeMCPServerScript(t *testing.T, description string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-mcp.sh")
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  [ -z "$id" ] && continue
  case "$line" in
    *initialize*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"fake","version":"1"},"capabilities":{}}}\n' "$id" ;;
    *tools/list*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","description":"` + description + `","inputSchema":{"type":"object"}}]}}\n' "$id" ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake server: %v", err)
	}
	return path
}

// mcpEnv points config.DefaultHome at a temp dir and writes a config with the
// given MCP servers, returning the config path.
func mcpEnv(t *testing.T, servers map[string]any) string {
	t.Helper()
	home := withTempHome(t)
	configPath := filepath.Join(home, "config.json")
	body, err := json.Marshal(map[string]any{
		"agents": map[string]any{"defaults": map[string]any{"workspace": filepath.Join(home, "workspace")}},
		"mcp":    map[string]any{"servers": servers},
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}

// runMCPCmd invokes one mcp subcommand action with the global flags the real
// app carries, and returns stdout plus the error.
func runMCPCmd(t *testing.T, action cli.ActionFunc, configPath string, args ...string) (string, error) {
	t.Helper()
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.PathFlag{Name: "config"},
			&cli.StringFlag{Name: "output", Value: string(output.Text)},
		},
		Action:         withJSONErrors(action),
		Writer:         io.Discard,
		ExitErrHandler: func(*cli.Context, error) {},
	}
	full := append([]string{"joshbot", "--config", configPath}, args...)
	var err error
	out := captureStdout(t, func() { err = app.Run(full) })
	return out, err
}

// mcpListDoc runs `mcp list --output json` and decodes the document.
func mcpListDoc(t *testing.T, configPath string) (output.MCPServers, string) {
	t.Helper()
	out, err := runMCPCmd(t, runMCPList, configPath, "--output", "json")
	if err != nil {
		t.Fatalf("mcp list: %v", err)
	}
	var doc output.MCPServers
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("mcp list --output json is not a document: %v\n%s", err, out)
	}
	return doc, out
}

// mcpServerEntry returns the named server from a listing document.
func mcpServerEntry(t *testing.T, doc output.MCPServers, name string) output.MCPServer {
	t.Helper()
	for _, s := range doc.Servers {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("server %q missing from listing: %+v", name, doc.Servers)
	return output.MCPServer{}
}

// TestMCPListClassifiesEveryServerState pins what an operator reads before
// approving anything. A reachable-but-unapproved server must read as pending
// (not approved), a broken one must be reported rather than sinking the
// listing, and a disabled one must not be spawned at all.
func TestMCPListClassifiesEveryServerState(t *testing.T) {
	script := fakeMCPServerScript(t, "echoes text back")
	configPath := mcpEnv(t, map[string]any{
		"fake":   map[string]any{"command": "/bin/sh", "args": []string{script}, "enabled": true},
		"broken": map[string]any{"command": "/nonexistent/mcp-server", "enabled": true},
		"off":    map[string]any{"command": "/bin/sh", "enabled": false},
	})

	doc, raw := mcpListDoc(t, configPath)

	fake := mcpServerEntry(t, doc, "fake")
	if fake.State != output.MCPPending {
		t.Fatalf("a reachable but unapproved server must be pending, got %q", fake.State)
	}
	if len(fake.Tools) != 1 || fake.Tools[0].Name != "echo" {
		t.Fatalf("listing must show what is being approved, got %+v", fake.Tools)
	}
	if broken := mcpServerEntry(t, doc, "broken"); broken.State != output.MCPUnreachable || broken.Error == "" {
		t.Fatalf("an unreachable server must be reported with its error, got %+v", broken)
	}
	if off := mcpServerEntry(t, doc, "off"); off.State != output.MCPDisabled {
		t.Fatalf("a disabled server must not be spawned, got %q", off.State)
	}
	if doc.Pending != 1 {
		t.Fatalf("pending count must cover only the awaiting-review server, got %d in %s", doc.Pending, raw)
	}
}

// TestMCPTrustApprovesThenUntrustRevokes walks the whole operator loop through
// the listing, which is the only thing that proves the CLI and the store agree
// on a server's state.
func TestMCPTrustApprovesThenUntrustRevokes(t *testing.T) {
	script := fakeMCPServerScript(t, "echoes text back")
	configPath := mcpEnv(t, map[string]any{
		"fake": map[string]any{"command": "/bin/sh", "args": []string{script}, "enabled": true},
	})

	if _, err := runMCPCmd(t, runMCPTrust, configPath, "fake"); err != nil {
		t.Fatalf("mcp trust: %v", err)
	}
	doc, _ := mcpListDoc(t, configPath)
	if got := mcpServerEntry(t, doc, "fake").State; got != output.MCPApproved {
		t.Fatalf("after trust the server must read approved, got %q", got)
	}
	if doc.Pending != 0 {
		t.Fatalf("nothing is awaiting review after approval, got %d", doc.Pending)
	}

	if _, err := runMCPCmd(t, runMCPUntrust, configPath, "fake"); err != nil {
		t.Fatalf("mcp untrust: %v", err)
	}
	doc, _ = mcpListDoc(t, configPath)
	if got := mcpServerEntry(t, doc, "fake").State; got != output.MCPPending {
		t.Fatalf("after untrust the server must read pending, got %q", got)
	}
}

// TestMCPTrustRefusesAServerItCannotRead is the core of the gate: recording a
// digest for a manifest that was never read would approve something sight
// unseen, which is the one outcome this command exists to prevent.
func TestMCPTrustRefusesAServerItCannotRead(t *testing.T) {
	configPath := mcpEnv(t, map[string]any{
		"broken": map[string]any{"command": "/nonexistent/mcp-server", "enabled": true},
	})

	if _, err := runMCPCmd(t, runMCPTrust, configPath, "broken"); err == nil {
		t.Fatal("trusting an unreachable server must fail; nothing was read to approve")
	}
	doc, _ := mcpListDoc(t, configPath)
	if got := mcpServerEntry(t, doc, "broken").State; got == output.MCPApproved {
		t.Fatal("a refused approval must leave no trust entry behind")
	}
}

// TestMCPTrustRejectsAnUnconfiguredName keeps the store from accumulating
// entries for servers that do not exist — an approval nobody can see in
// `mcp list` is an approval nobody will ever revoke.
func TestMCPTrustRejectsAnUnconfiguredName(t *testing.T) {
	configPath := mcpEnv(t, map[string]any{})

	if _, err := runMCPCmd(t, runMCPTrust, configPath, "ghost"); err == nil {
		t.Fatal("trusting a server that is not configured must fail")
	}
	if _, err := runMCPCmd(t, runMCPTrust, configPath); err == nil {
		t.Fatal("trust with no server name must fail rather than approve something")
	}
	if _, err := runMCPCmd(t, runMCPUntrust, configPath); err == nil {
		t.Fatal("untrust with no server name must fail")
	}
}

// TestMCPListWithNoServersEmitsAnEmptyArray pins the shape of the ordinary
// first-run document: a nil slice encodes as `null` and breaks a consumer
// iterating doc["servers"], for the least exceptional state there is.
func TestMCPListWithNoServersEmitsAnEmptyArray(t *testing.T) {
	configPath := mcpEnv(t, map[string]any{})

	_, raw := mcpListDoc(t, configPath)
	if !strings.Contains(raw, `"servers": []`) && !strings.Contains(raw, `"servers":[]`) {
		t.Fatalf("no servers configured must encode as an empty array, got %s", raw)
	}
}

// TestMCPListJSONDoesNotLeakACredential pins the redaction boundary for this
// command. A tool description is server-supplied text that lands in the
// document verbatim, and the JSON path deliberately bypasses the redacting
// writer (it would corrupt the encoding), so redaction has to happen at
// construction or not at all.
func TestMCPListJSONDoesNotLeakACredential(t *testing.T) {
	script := fakeMCPServerScript(t, "run with api_key=sk-live-abcdef1234567890")
	configPath := mcpEnv(t, map[string]any{
		"fake": map[string]any{"command": "/bin/sh", "args": []string{script}, "enabled": true},
	})

	_, raw := mcpListDoc(t, configPath)
	if strings.Contains(raw, "sk-live-abcdef1234567890") {
		t.Fatalf("a credential in server-supplied text reached the JSON document: %s", raw)
	}
}

// TestMCPListTextRendersEveryState guards the human-facing rendering, which is
// what an operator actually reads before typing `mcp trust`.
func TestMCPListTextRendersEveryState(t *testing.T) {
	script := fakeMCPServerScript(t, "echoes text back")
	configPath := mcpEnv(t, map[string]any{
		"fake": map[string]any{"command": "/bin/sh", "args": []string{script}, "enabled": true},
	})

	out, err := runMCPCmd(t, runMCPList, configPath)
	if err != nil {
		t.Fatalf("mcp list: %v", err)
	}
	for _, want := range []string{"fake", "echo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("text listing must name the server and its tools, missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "joshbot mcp trust") {
		t.Fatalf("a pending server must tell the operator how to approve it:\n%s", out)
	}
}
