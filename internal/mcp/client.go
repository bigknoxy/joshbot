package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// shutdownGrace is how long Close waits for a server to exit after its stdin is
// closed before it is killed. Kept short so shutdown never blocks the process.
const shutdownGrace = 3 * time.Second

// Server describes a stdio MCP server to spawn. It is transport/config agnostic
// so callers can build it from joshbot config, a test, or anywhere else.
type Server struct {
	// Name is the operator-chosen identifier, used to namespace the server's
	// tools so they cannot shadow a built-in tool.
	Name string
	// Command is the executable to run.
	Command string
	// Args are the command-line arguments.
	Args []string
	// Env are extra environment variables (KEY=VALUE) for the child process.
	// They are appended to the current environment.
	Env []string
}

// Client is a stdio MCP client bound to a single server process. It starts the
// process lazily on the first Connect and keeps it running for reuse. All
// exported methods are safe for concurrent use.
type Client struct {
	server Server

	// startMu serializes Connect/Close so the process is created and reaped
	// exactly once.
	startMu sync.Mutex
	started bool
	closed  bool

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	writeM sync.Mutex // serializes writes to stdin

	// pending maps a request id to the channel awaiting its response.
	pendMu  sync.Mutex
	nextID  int64
	pending map[int64]chan *rpcResponse

	// readErr records why the reader goroutine stopped; readers see it once
	// doneCh is closed.
	doneCh  chan struct{}
	readErr error
}

// NewClient returns a client for the given server. No process is started until
// Connect is called.
func NewClient(server Server) *Client {
	return &Client{
		server:  server,
		pending: make(map[int64]chan *rpcResponse),
		doneCh:  make(chan struct{}),
	}
}

// Name returns the server's configured name.
func (c *Client) Name() string { return c.server.Name }

// Connect starts the server process if it is not already running and completes
// the MCP initialize handshake. It is idempotent and safe to call repeatedly.
// The ctx bounds only the handshake; the process outlives it.
func (c *Client) Connect(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()

	if c.closed {
		return errors.New("mcp: client is closed")
	}
	if c.started {
		return nil
	}

	if c.server.Command == "" {
		return fmt.Errorf("mcp: server %q has no command", c.server.Name)
	}

	// exec.Command (not CommandContext): the process lifetime is tied to the
	// Client, not to the ctx of whichever call happened to start it. Cancelling
	// a single tool call must not kill a shared server.
	cmd := exec.Command(c.server.Command, c.server.Args...)
	if len(c.server.Env) > 0 {
		cmd.Env = append(cmd.Environ(), c.server.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp: stdin pipe for %q: %w", c.server.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp: stdout pipe for %q: %w", c.server.Name, err)
	}
	// Discard stderr rather than inheriting it, so a chatty server does not
	// corrupt joshbot's own stdout/stderr. (Left nil == inherit.)
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp: start %q (%s): %w", c.server.Name, c.server.Command, err)
	}

	c.cmd = cmd
	c.stdin = stdin
	c.started = true

	go c.readLoop(stdout)

	if err := c.handshake(ctx); err != nil {
		// Roll back a failed start so we do not leak the process.
		c.shutdownProcess()
		c.started = false
		return err
	}
	return nil
}

// handshake performs initialize + notifications/initialized.
func (c *Client) handshake(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      clientInfo{Name: "joshbot", Version: "1"},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("mcp: initialize %q: %w", c.server.Name, err)
	}
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("mcp: initialized notify %q: %w", c.server.Name, err)
	}
	return nil
}

// ListTools returns the tools advertised by the server. Connect must have
// succeeded first (RegisterMCPTools calls Connect for you).
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res listToolsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp: decode tools/list from %q: %w", c.server.Name, err)
	}
	return res.Tools, nil
}

// CallTool invokes a tool on the server and returns its rendered text output.
// A tool that reports isError returns a non-nil error carrying the text, so the
// agent sees it as a tool failure rather than a success.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.call(ctx, "tools/call", callToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}
	var res callToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("mcp: decode tools/call from %q: %w", c.server.Name, err)
	}

	var b strings.Builder
	for _, block := range res.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	text := b.String()
	if res.IsError {
		if text == "" {
			text = "tool reported an error"
		}
		return "", fmt.Errorf("mcp tool %q error: %s", name, text)
	}
	return text, nil
}

// call sends a JSON-RPC request and waits for the matching response, the ctx
// deadline, or the reader goroutine stopping — whichever comes first. A ctx
// timeout is what keeps a hung server from hanging the agent.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.pendMu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan *rpcResponse, 1)
	c.pending[id] = ch
	c.pendMu.Unlock()

	cleanup := func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}

	req := rpcRequest{JSONRPC: jsonrpcVersion, ID: &id, Method: method, Params: params}
	if err := c.writeMessage(req); err != nil {
		cleanup()
		return nil, err
	}

	select {
	case <-ctx.Done():
		cleanup()
		return nil, fmt.Errorf("mcp: %s on %q: %w", method, c.server.Name, ctx.Err())
	case <-c.doneCh:
		cleanup()
		return nil, fmt.Errorf("mcp: %s on %q: server stopped: %w", method, c.server.Name, c.readErr)
	case resp := <-ch:
		cleanup()
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp: %s on %q: %s", method, c.server.Name, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (c *Client) notify(method string, params any) error {
	return c.writeMessage(rpcRequest{JSONRPC: jsonrpcVersion, Method: method, Params: params})
}

// writeMessage marshals and writes one newline-delimited JSON message.
func (c *Client) writeMessage(msg rpcRequest) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("mcp: marshal %s: %w", msg.Method, err)
	}
	data = append(data, '\n')

	c.writeM.Lock()
	defer c.writeM.Unlock()
	if c.stdin == nil {
		return errors.New("mcp: client not connected")
	}
	if _, err := c.stdin.Write(data); err != nil {
		return fmt.Errorf("mcp: write %s to %q: %w", msg.Method, c.server.Name, err)
	}
	return nil
}

// readLoop reads newline-delimited JSON from the server until EOF or error,
// dispatching each response to its waiting caller. It closes doneCh on exit so
// pending and future calls fail fast instead of blocking forever.
func (c *Client) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	var loopErr error
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				loopErr = err
			} else {
				loopErr = io.EOF
			}
			break
		}
	}

	c.pendMu.Lock()
	c.readErr = loopErr
	// Wake every pending caller; doneCh close below is the broadcast.
	c.pendMu.Unlock()
	close(c.doneCh)
}

// dispatch routes one raw message line to its pending caller. Notifications
// (nil id) and unmatched ids are dropped.
func (c *Client) dispatch(line []byte) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return
	}
	var resp rpcResponse
	if err := json.Unmarshal([]byte(trimmed), &resp); err != nil {
		return // not a JSON-RPC message we understand; ignore
	}
	if resp.ID == nil {
		return // notification
	}
	c.pendMu.Lock()
	ch, ok := c.pending[*resp.ID]
	c.pendMu.Unlock()
	if ok {
		ch <- &resp
	}
}

// Close shuts the server down cleanly: it closes stdin (the graceful signal for
// a stdio server to exit), waits shutdownGrace, then kills the process, and in
// all cases reaps it with Wait so no zombie is left behind. Idempotent.
func (c *Client) Close() error {
	c.startMu.Lock()
	defer c.startMu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	if !c.started {
		return nil
	}
	c.shutdownProcess()
	return nil
}

// shutdownProcess tears down the running process. Caller holds startMu.
func (c *Client) shutdownProcess() {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}

	waited := make(chan error, 1)
	go func() { waited <- c.cmd.Wait() }()

	select {
	case <-waited:
	case <-time.After(shutdownGrace):
		_ = c.cmd.Process.Kill()
		<-waited // reap to avoid a zombie
	}
}
