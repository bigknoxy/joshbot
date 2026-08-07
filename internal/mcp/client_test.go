package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMain lets this test binary double as a fake MCP server when the
// GO_MCP_HELPER env var is set. Each helper mode implements just enough of the
// protocol to exercise one client behaviour.
func TestMain(m *testing.M) {
	switch os.Getenv("GO_MCP_HELPER") {
	case "":
		os.Exit(m.Run())
	case "echo":
		runEchoServer()
	case "hang":
		runHangServer()
	case "nohandshake":
		// Exit immediately without responding to initialize.
		os.Exit(0)
	}
	os.Exit(0)
}

// helperClient builds a client that re-execs this test binary in the named
// helper mode.
func helperClient(t *testing.T, mode string) *Client {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return NewClient(Server{
		Name:    "test",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=" + mode},
	})
}

// runEchoServer is a minimal MCP server: it answers initialize, advertises one
// "echo" tool via tools/list, and echoes the "text" argument on tools/call.
func runEchoServer() {
	in := bufio.NewReader(os.Stdin)
	out := os.Stdout
	for {
		line, err := in.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			if err != nil {
				return
			}
			continue
		}
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(trimmed), &req) != nil {
			continue
		}
		if req.ID == nil {
			// notification (e.g. notifications/initialized): no reply
			if err != nil {
				return
			}
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "echo", "version": "1"},
				"capabilities":    map[string]any{},
			}
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{{
					"name":        "echo",
					"description": "echoes text back",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
						"required":   []string{"text"},
					},
				}},
			}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name == "boom" {
				result = map[string]any{
					"content": []map[string]any{{"type": "text", "text": "kaboom"}},
					"isError": true,
				}
			} else {
				text, _ := p.Arguments["text"].(string)
				result = map[string]any{
					"content": []map[string]any{{"type": "text", "text": text}},
				}
			}
		default:
			result = map[string]any{}
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result}
		data, _ := json.Marshal(resp)
		fmt.Fprintf(out, "%s\n", data)
		if err != nil {
			return
		}
	}
}

// runHangServer answers initialize but never responds to tools/list — used to
// verify the client's context timeout.
func runHangServer() {
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			var req struct {
				ID     *int64 `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal([]byte(trimmed), &req) == nil && req.ID != nil && req.Method == "initialize" {
				resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", *req.ID)
				os.Stdout.WriteString(resp)
			}
			// any other request: hang (no reply)
		}
		if err != nil {
			return
		}
	}
}

func TestClientListAndCall(t *testing.T) {
	c := helperClient(t, "echo")
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	out, err := c.CallTool(ctx, "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if out != "hello" {
		t.Fatalf("expected echo 'hello', got %q", out)
	}
}

func TestClientToolError(t *testing.T) {
	c := helperClient(t, "echo")
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_, err := c.CallTool(ctx, "boom", nil)
	if err == nil {
		t.Fatal("expected error from isError tool result")
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected error text to carry tool message, got %v", err)
	}
}

func TestClientConnectIdempotent(t *testing.T) {
	c := helperClient(t, "echo")
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect 1: %v", err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect 2 (should be no-op): %v", err)
	}
}

func TestClientCallTimeout(t *testing.T) {
	c := helperClient(t, "hang")
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	callCtx, callCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer callCancel()
	_, err := c.ListTools(callCtx)
	if err == nil {
		t.Fatal("expected timeout error from hanging server")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected deadline error, got %v", err)
	}
}

func TestConnectFailsForMissingCommand(t *testing.T) {
	c := NewClient(Server{Name: "bad", Command: "/nonexistent/joshbot-mcp-xyz"})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err == nil {
		t.Fatal("expected connect to fail for missing command")
	}
}

func TestCloseIsIdempotentAndReaps(t *testing.T) {
	c := helperClient(t, "echo")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close 2 (idempotent): %v", err)
	}
	// After close, calls fail fast rather than hang.
	if _, err := c.ListTools(ctx); err == nil {
		t.Fatal("expected error calling a closed client")
	}
}

func TestManagerClose(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	spec := Server{Name: "m", Command: exe, Args: []string{"-test.run=TestMain"}, Env: []string{"GO_MCP_HELPER=echo"}}
	mgr := NewManager([]Server{spec})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, c := range mgr.Clients() {
		if err := c.Connect(ctx); err != nil {
			t.Fatalf("Connect: %v", err)
		}
	}
	mgr.Close() // must not panic or hang
	_ = exec.Command
}
