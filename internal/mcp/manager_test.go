package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/log"
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

	// The stability window elapses at once: what this test is about is the
	// reconnect, not the window.
	mgr.afterFn = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}

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
	if st[0].Attempts != 0 { // cleared once the stability window has elapsed
		t.Errorf("expected Attempts=0 after success, got %d", st[0].Attempts)
	}
}

// fakeWindow is the injectable stability-window timer. It fires only when the
// test releases it, so a test can hold the window open — the server dies
// inside it — or let it elapse at once.
type fakeWindow struct {
	mu       sync.Mutex
	pending  []chan time.Time
	requests chan time.Duration
}

func newFakeWindow() *fakeWindow {
	return &fakeWindow{requests: make(chan time.Duration, 16)}
}

// after is the manager's afterFn: it records the window it was asked to wait
// out and returns a channel that fires when the test releases it.
func (w *fakeWindow) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	w.mu.Lock()
	w.pending = append(w.pending, ch)
	w.mu.Unlock()
	select {
	case w.requests <- d:
	default:
	}
	return ch
}

// release lets every requested timer fire.
func (w *fakeWindow) release() {
	w.mu.Lock()
	pending := w.pending
	w.pending = nil
	w.mu.Unlock()
	for _, ch := range pending {
		ch <- time.Now()
	}
}

// fakeBackoff records the delays the manager waits out between reconnect
// attempts and returns at once, so a test never sleeps for wall-clock time.
type fakeBackoff struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (b *fakeBackoff) sleep(d time.Duration) <-chan time.Time {
	b.mu.Lock()
	b.delays = append(b.delays, d)
	b.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

func (b *fakeBackoff) recorded() []time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]time.Duration(nil), b.delays...)
}

// TestManagerFlappingServerReachesMaxAttempts is the regression test for the
// attempt count that was cleared at every successful handshake: a server that
// came up and died again straight away was reconnected every initial
// interval, forever, because each successful reconnect zeroed the count.
func TestManagerFlappingServerReachesMaxAttempts(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	spec := Server{
		Name:    "flapper",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=flapper"},
	}

	cfg := ReconnectConfig{
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		MaxAttempts:     3,
		JitterFraction:  0,
		StabilityWindow: time.Minute,
	}
	mgr := NewManagerWithConfig([]Server{spec}, cfg)
	defer mgr.Close()

	backoff := &fakeBackoff{}
	mgr.sleepFn = backoff.sleep
	// The stability window never elapses: the server dies inside it.
	mgr.afterFn = newFakeWindow().after

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr.handleServerDeath(ctx, mgr.Client("flapper"))

	st := mgr.Status()
	if st[0].Attempts != cfg.MaxAttempts {
		t.Errorf("expected Attempts=%d once the flapping server reached the cap, got %d", cfg.MaxAttempts, st[0].Attempts)
	}
	if st[0].Reconnecting {
		t.Errorf("expected Reconnecting=false once the manager gave up")
	}
	if st[0].Connected {
		t.Errorf("expected Connected=false: the flapping server is not up")
	}
	if !strings.Contains(st[0].LastError, "could not be kept up") {
		t.Errorf("expected the status to say the server could not be kept up, got %q", st[0].LastError)
	}

	// The backoff grew across the flaps: the attempt count survived every
	// successful handshake, so the delay was not stuck at the initial interval.
	want := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}
	got := backoff.recorded()
	if len(got) != len(want) {
		t.Fatalf("expected %d reconnect attempts, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("backoff %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

// TestManagerStableServerForgivesAttempts proves the other half of the
// stability window: a server that holds the connection past the window is a
// normal healthy server again, so its next failure starts from the initial
// interval instead of a stale large delay.
func TestManagerStableServerForgivesAttempts(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	spec := Server{
		Name:    "steady",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=echo"},
	}

	cfg := ReconnectConfig{
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		MaxAttempts:     3,
		JitterFraction:  0,
		StabilityWindow: time.Minute,
	}
	mgr := NewManagerWithConfig([]Server{spec}, cfg)
	defer mgr.Close()

	mgr.sleepFn = (&fakeBackoff{}).sleep
	window := newFakeWindow()
	mgr.afterFn = window.after

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.handleServerDeath(ctx, mgr.Client("steady"))
	}()

	// Wait until the manager has reconnected and is watching the window.
	select {
	case d := <-window.requests:
		if d != cfg.StabilityWindow {
			t.Fatalf("expected the manager to watch a %v stability window, got %v", cfg.StabilityWindow, d)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the manager never reached the stability window")
	}

	// Before the window elapses, the attempt that got the server here is
	// still charged to it.
	if got := mgr.Status()[0].Attempts; got != 1 {
		t.Fatalf("expected Attempts=1 while the stability window is open, got %d", got)
	}

	window.release()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the manager did not finish the reconnect after the stability window elapsed")
	}

	st := mgr.Status()
	if st[0].Attempts != 0 {
		t.Errorf("expected Attempts=0 after the server held the connection for the stability window, got %d", st[0].Attempts)
	}
	if !st[0].Connected {
		t.Errorf("expected Connected=true: the server held the connection")
	}
	if st[0].Reconnecting {
		t.Errorf("expected Reconnecting=false once the server counted as recovered")
	}
}

// TestManagerGiveUpReportsAttemptCount pins what the operator sees when the
// cap is reached: the server could not be kept up, and how many attempts it
// took — in the log and in the manager's status.
func TestManagerGiveUpReportsAttemptCount(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	spec := Server{
		Name:    "flapper",
		Command: exe,
		Args:    []string{"-test.run=TestMain"},
		Env:     []string{"GO_MCP_HELPER=flapper"},
	}

	cfg := ReconnectConfig{
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		MaxAttempts:     2,
		JitterFraction:  0,
		StabilityWindow: time.Minute,
	}
	mgr := NewManagerWithConfig([]Server{spec}, cfg)
	defer mgr.Close()

	mgr.sleepFn = (&fakeBackoff{}).sleep
	mgr.afterFn = newFakeWindow().after

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr.handleServerDeath(ctx, mgr.Client("flapper"))

	out := logged.String()
	if !strings.Contains(out, "could not be kept up") {
		t.Errorf("expected the log to say the server could not be kept up, got:\n%s", out)
	}
	if !strings.Contains(out, "attempts=2") {
		t.Errorf("expected the log to carry the attempt count, got:\n%s", out)
	}

	st := mgr.Status()
	if !strings.Contains(st[0].LastError, "could not be kept up after 2 reconnect attempts") {
		t.Errorf("expected the status to carry the attempt count, got %q", st[0].LastError)
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
