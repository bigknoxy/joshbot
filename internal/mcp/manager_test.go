package mcp

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestManagerStatusAndObservability(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	spec := Server{
		Name:    "echo-server",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=echo"},
	}

	mgr := NewManager([]Server{spec})
	defer mgr.Close()

	// Initial status: not connected
	st := mgr.Status()
	if len(st) != 1 {
		t.Fatalf("expected 1 status entry, got %d", len(st))
	}
	if st[0].Name != "echo-server" {
		t.Errorf("expected name echo-server, got %s", st[0].Name)
	}
	if st[0].Connected {
		t.Errorf("expected Connected=false before Connect")
	}

	// Connect
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mgr.Client("echo-server")
	if client == nil {
		t.Fatal("Client('echo-server') returned nil")
	}
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	st = mgr.Status()
	if !st[0].Connected {
		t.Errorf("expected Connected=true after Connect")
	}
}

func TestManagerBackoffCalculation(t *testing.T) {
	cfg := ReconnectConfig{
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     1 * time.Second,
		MaxAttempts:     5,
		JitterFraction:  0, // no jitter for deterministic check
	}
	mgr := NewManagerWithConfig(nil, cfg)

	d0 := mgr.calculateBackoff(0)
	if d0 != 100*time.Millisecond {
		t.Errorf("attempt 0 backoff: got %v, want 100ms", d0)
	}

	d1 := mgr.calculateBackoff(1)
	if d1 != 200*time.Millisecond {
		t.Errorf("attempt 1 backoff: got %v, want 200ms", d1)
	}

	d2 := mgr.calculateBackoff(2)
	if d2 != 400*time.Millisecond {
		t.Errorf("attempt 2 backoff: got %v, want 400ms", d2)
	}

	d3 := mgr.calculateBackoff(3)
	if d3 != 800*time.Millisecond {
		t.Errorf("attempt 3 backoff: got %v, want 800ms", d3)
	}

	d4 := mgr.calculateBackoff(4)
	if d4 != 1*time.Second { // clamped at max 1s
		t.Errorf("attempt 4 backoff: got %v, want 1s", d4)
	}

	// Test jitter fraction bounds
	cfgJitter := ReconnectConfig{
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     1 * time.Second,
		MaxAttempts:     5,
		JitterFraction:  0.25,
	}
	mgrJitter := NewManagerWithConfig(nil, cfgJitter)
	for i := 0; i < 20; i++ {
		dj := mgrJitter.calculateBackoff(0)
		if dj < 100*time.Millisecond || dj > 125*time.Millisecond {
			t.Errorf("jittered backoff out of [100ms, 125ms]: %v", dj)
		}
	}
}

func TestManagerReconnectsKilledServer(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	spec := Server{
		Name:    "resilient",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=echo"},
	}

	cfg := ReconnectConfig{
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		MaxAttempts:     5,
		JitterFraction:  0.1,
	}
	mgr := NewManagerWithConfig([]Server{spec}, cfg)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mgr.Client("resilient")
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("initial Connect: %v", err)
	}

	// Verify it can call tool
	out, err := client.CallTool(ctx, "echo", map[string]any{"text": "before-kill"})
	if err != nil || out != "before-kill" {
		t.Fatalf("call before kill: out=%q, err=%v", out, err)
	}

	// Start monitor
	mgr.StartMonitor(ctx)

	// Kill the child process directly
	sess := client.currentSession()
	if sess.cmd != nil && sess.cmd.Process != nil {
		_ = sess.cmd.Process.Kill()
	}

	// Wait for process death and then automatic reconnect
	deadline := time.Now().Add(5 * time.Second)
	// First wait until it is observed disconnected
	for time.Now().Before(deadline) {
		if !client.IsConnected() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Then wait until it is reconnected
	reconnected := false
	for time.Now().Before(deadline) {
		if client.IsConnected() {
			reconnected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !reconnected {
		t.Fatal("server was killed but manager did not reconnect it within deadline")
	}

	// Verify tool call works on the reconnected process
	outAfter, err := client.CallTool(ctx, "echo", map[string]any{"text": "after-reconnect"})
	if err != nil || outAfter != "after-reconnect" {
		t.Fatalf("CallTool after reconnect failed: out=%q, err=%v", outAfter, err)
	}
}

func TestManagerPermanentlyDeadServerDoesNotSpinOrBlock(t *testing.T) {
	// A server that always exits immediately (nohandshake)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	spec := Server{
		Name:    "doomed",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=nohandshake"},
	}

	cfg := ReconnectConfig{
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		MaxAttempts:     3,
		JitterFraction:  0,
	}
	mgr := NewManagerWithConfig([]Server{spec}, cfg)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := mgr.Client("doomed")
	// Initial Connect will fail
	_ = client.Connect(ctx)

	// Simulate death handling
	mgr.handleServerDeath(ctx, client)

	st := mgr.Status()
	if len(st) != 1 {
		t.Fatalf("expected 1 status entry, got %d", len(st))
	}
	if st[0].Connected {
		t.Errorf("expected Connected=false")
	}
	if st[0].Reconnecting {
		t.Errorf("expected Reconnecting=false after reaching MaxAttempts")
	}
	if st[0].Attempts != 3 {
		t.Errorf("expected Attempts=3, got %d", st[0].Attempts)
	}
	if st[0].LastError == "" {
		t.Errorf("expected non-empty LastError")
	}
}

func TestManagerFlakyServerReconnectsAfterFailedAttempts(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "flaky-reconnect-marker")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// Flaky helper fails first time, succeeds second time
	spec := Server{
		Name:    "flaky",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=flaky", "GO_MCP_MARKER=" + marker},
	}

	cfg := ReconnectConfig{
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		MaxAttempts:     5,
		JitterFraction:  0,
	}
	mgr := NewManagerWithConfig([]Server{spec}, cfg)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := mgr.Client("flaky")

	// Trigger reconnect attempt loop
	mgr.handleServerDeath(ctx, client)

	if !client.IsConnected() {
		t.Fatal("expected flaky server to eventually reconnect")
	}

	st := mgr.Status()
	if !st[0].Connected {
		t.Errorf("expected Connected=true")
	}
	if st[0].Attempts != 0 { // reset to 0 on successful reconnect
		t.Errorf("expected Attempts=0 after success, got %d", st[0].Attempts)
	}
}

func TestManagerFastSimulatedClock(t *testing.T) {
	// Verify that custom now and sleepFn can drive retries without wall-clock sleep
	simTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var sleepCalls []time.Duration
	var mu sync.Mutex

	cfg := ReconnectConfig{
		InitialInterval: 1 * time.Second,
		MaxInterval:     10 * time.Second,
		MaxAttempts:     3,
		JitterFraction:  0,
	}
	mgr := NewManagerWithConfig([]Server{{Name: "fake", Command: "/nonexistent"}}, cfg)
	mgr.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return simTime
	}
	mgr.sleepFn = func(d time.Duration) <-chan time.Time {
		mu.Lock()
		sleepCalls = append(sleepCalls, d)
		simTime = simTime.Add(d)
		mu.Unlock()
		ch := make(chan time.Time, 1)
		ch <- simTime
		return ch
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := mgr.Client("fake")
	mgr.handleServerDeath(ctx, client)

	mu.Lock()
	defer mu.Unlock()
	if len(sleepCalls) != 3 {
		t.Fatalf("expected 3 sleep calls, got %d: %v", len(sleepCalls), sleepCalls)
	}
	if sleepCalls[0] != 1*time.Second {
		t.Errorf("sleep 0: got %v, want 1s", sleepCalls[0])
	}
	if sleepCalls[1] != 2*time.Second {
		t.Errorf("sleep 1: got %v, want 2s", sleepCalls[1])
	}
	if sleepCalls[2] != 4*time.Second {
		t.Errorf("sleep 2: got %v, want 4s", sleepCalls[2])
	}

	st := mgr.Status()
	if st[0].Attempts != 3 {
		t.Errorf("expected Attempts=3, got %d", st[0].Attempts)
	}
}

func TestManagerCloseStopsMonitorAndReaps(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	spec := Server{
		Name:    "to-close",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=echo"},
	}

	mgr := NewManager([]Server{spec})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := mgr.Client("to-close")
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	mgr.StartMonitor(ctx)
	mgr.Close()

	if !client.IsClosed() {
		t.Error("expected client to be closed after mgr.Close()")
	}
	if !mgr.isClosed() {
		t.Error("expected manager to be closed")
	}

	// Repeated Close is safe and idempotent
	mgr.Close()
}
