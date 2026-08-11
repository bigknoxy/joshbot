package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	case "flaky":
		// Fail the first process (no handshake), serve normally after that.
		// The marker file is what distinguishes the runs, since a Client's
		// command and env are fixed at construction.
		marker := os.Getenv("GO_MCP_MARKER")
		if _, err := os.Stat(marker); err != nil {
			_ = os.WriteFile(marker, []byte("x"), 0o600)
			os.Exit(0)
		}
		runEchoServer()
	case "dupe":
		runDuplicateResponseServer()
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
			} else if p.Name == "env" {
				result = map[string]any{
					"content": []map[string]any{{"type": "text", "text": strings.Join(os.Environ(), "\n")}},
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

// runDuplicateResponseServer answers initialize normally, then answers every
// tools/list twice with the same id — the hostile-server shape that used to
// wedge the read loop on a blocking send into a full cap-1 channel.
func runDuplicateResponseServer() {
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			var req struct {
				ID     *int64 `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal([]byte(trimmed), &req) == nil && req.ID != nil {
				var result string
				switch req.Method {
				case "tools/list":
					result = `{"tools":[{"name":"echo","description":"d"}]}`
				case "tools/call":
					result = `{"content":[{"type":"text","text":"ok"}]}`
				default:
					result = `{}`
				}
				resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", *req.ID, result)
				os.Stdout.WriteString(resp)
				if req.Method == "tools/list" {
					// The duplicate: same id, again.
					os.Stdout.WriteString(resp)
				}
			}
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

// TestChildDoesNotInheritParentSecrets pins the credential boundary around a
// spawned MCP server: it is third-party code, so joshbot's own provider keys
// must not be in its environment. Only what the operator listed in Server.Env
// gets through.
func TestChildDoesNotInheritParentSecrets(t *testing.T) {
	t.Setenv("JOSHBOT_PROVIDERS__OPENROUTER__API_KEY", "sk-must-not-leak")
	t.Setenv("OPENAI_API_KEY", "sk-also-must-not-leak")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	c := NewClient(Server{
		Name:    "test",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=echo", "MY_SERVER_TOKEN=explicitly-granted"},
	})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	env, err := c.CallTool(ctx, "env", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	for _, leaked := range []string{"sk-must-not-leak", "sk-also-must-not-leak", "OPENAI_API_KEY"} {
		if strings.Contains(env, leaked) {
			t.Errorf("child environment leaked %q:\n%s", leaked, env)
		}
	}
	if !strings.Contains(env, "MY_SERVER_TOKEN=explicitly-granted") {
		t.Errorf("explicit Server.Env entry did not reach the child:\n%s", env)
	}
	if !strings.Contains(env, "PATH=") {
		t.Errorf("sanitized base environment is missing PATH:\n%s", env)
	}
}

// TestReadLineRejectsOversizedMessage covers the heap bound: a server that
// never emits a newline must fail the connection, not grow a buffer forever.
func TestReadLineRejectsOversizedMessage(t *testing.T) {
	flood := strings.NewReader(strings.Repeat("x", maxMessageBytes+1024))
	if _, err := readLine(bufio.NewReader(flood)); !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("readLine err = %v, want errMessageTooLarge", err)
	}

	ok := strings.NewReader("hello\nworld\n")
	r := bufio.NewReader(ok)
	line, err := readLine(r)
	if err != nil || string(line) != "hello\n" {
		t.Fatalf("readLine = %q, %v; want \"hello\\n\", nil", line, err)
	}
}

// helperClientWithEnv is helperClient with extra environment for the child.
func helperClientWithEnv(t *testing.T, mode string, extra ...string) *Client {
	t.Helper()
	c := helperClient(t, mode)
	c.server.Env = append(c.server.Env, extra...)
	return c
}

// TestConnectFailsWhenServerExitsBeforeHandshake pins that a server dying
// before it answers initialize surfaces promptly as a Connect error, and that a
// later call errors rather than blocking until its own deadline.
func TestConnectFailsWhenServerExitsBeforeHandshake(t *testing.T) {
	c := helperClient(t, "nohandshake")
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := c.Connect(ctx); err == nil {
		t.Fatal("expected Connect to fail when the server exits before handshake")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Connect took %v; it should fail as soon as the process exits", elapsed)
	}

	done := make(chan error, 1)
	go func() {
		_, err := c.ListTools(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ListTools to fail on a client that never connected")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListTools blocked instead of failing fast")
	}
}

// TestReconnectAfterFailedHandshake is the regression test for a client wedged
// by its own failed start. doneCh used to be a per-Client field: the failed
// process's readLoop closed it, nothing reallocated it, and every call on the
// next — successful — process took the "server stopped" branch forever, so the
// client reported healthy and answered nothing.
func TestReconnectAfterFailedHandshake(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started-once")
	c := helperClientWithEnv(t, "flaky", "GO_MCP_MARKER="+marker)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := c.Connect(ctx); err == nil {
		t.Fatal("expected the first Connect to fail (server exits before handshake)")
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("second Connect: %v", err)
	}

	out, err := c.CallTool(ctx, "echo", map[string]any{"text": "alive"})
	if err != nil {
		t.Fatalf("CallTool after reconnect: %v", err)
	}
	if out != "alive" {
		t.Fatalf("CallTool after reconnect returned %q, want %q", out, "alive")
	}
}

// TestDuplicateResponseDoesNotWedgeClient is the regression test for a hostile
// server answering the same JSON-RPC id twice. dispatch is exercised directly,
// because an end-to-end call cannot reproduce the wedge: call() removes its
// pending entry as soon as it returns, so by the time the duplicate arrives
// there is nothing left to send to. The wedge needs a pending entry whose
// cap-1 channel is full and undrained — exactly the state a caller that has
// already given up on its ctx (but not yet run cleanup) leaves behind. Two
// halves of the fix are pinned here: the delete-under-pendMu (the id must be
// claimed, so the second dispatch finds nothing) and the non-blocking send (so
// even if it did find the entry, readLoop is not parked on it forever).
func TestDuplicateResponseDoesNotWedgeClient(t *testing.T) {
	c := NewClient(Server{Name: "dupe"})

	const id = int64(7)
	ch := make(chan *rpcResponse, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()

	line := []byte(`{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"ok"}]}}`)

	// First dispatch fills the cap-1 channel. Nobody ever drains it: this is
	// the abandoned-caller state.
	c.dispatch(line)

	c.pendMu.Lock()
	_, stillPending := c.pending[id]
	c.pendMu.Unlock()
	if stillPending {
		t.Fatal("dispatch left the pending entry in place; a duplicate response can be delivered twice")
	}

	// The duplicate must not block. Run it off-goroutine so a blocking send
	// fails the test on a deadline instead of hanging the whole package.
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.dispatch(line)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second dispatch of the same id blocked: readLoop is wedged, and with it every in-flight and future call")
	}

	// Sanity: the response really was delivered once, to the original waiter.
	select {
	case resp := <-ch:
		if resp == nil || resp.ID == nil || *resp.ID != id {
			t.Fatalf("waiter received the wrong response: %+v", resp)
		}
	default:
		t.Fatal("the first dispatch delivered nothing to the waiting caller")
	}

	// And an unmatched id is simply dropped, without blocking.
	c.dispatch([]byte(`{"jsonrpc":"2.0","id":999,"result":{}}`))

	// Second half of the fix, pinned on its own: a pending entry whose cap-1
	// channel is already full. delete alone does not save readLoop here — the
	// send happens after the claim, and a blocking one parks forever.
	const id2 = int64(8)
	full := make(chan *rpcResponse, 1)
	full <- &rpcResponse{}
	c.pendMu.Lock()
	c.pending[id2] = full
	c.pendMu.Unlock()

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		c.dispatch([]byte(`{"jsonrpc":"2.0","id":8,"result":{}}`))
	}()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch blocked sending to an abandoned caller's full channel: readLoop is wedged")
	}
}

// TestConcurrentCallsGetOwnResults pins response routing under concurrency: a
// caller must never receive another caller's result. Run with -race.
func TestConcurrentCallsGetOwnResults(t *testing.T) {
	c := helperClient(t, "echo")
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("call-%d", i)
			out, err := c.CallTool(ctx, "echo", map[string]any{"text": want})
			if err != nil {
				errs <- fmt.Errorf("call %d: %w", i, err)
				return
			}
			if out != want {
				errs <- fmt.Errorf("call %d got %q, want %q", i, out, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
