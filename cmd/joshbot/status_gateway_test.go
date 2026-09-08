package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/gatewaystatus"
)

// writeConfigAt writes config.json into dir and returns its path.
func writeConfigAt(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestStatusJSON_ReportsLiveGatewayState pins that runStatus reads the gateway
// status file the running gateway writes (issue #389): a connected Telegram
// channel shows up in the JSON document as gateway_running=true with the
// per-channel live state — not just the config's telegram_enabled boolean.
//
// The config and the gateway status file live in the SAME home directory,
// because loadConfig anchors config.DefaultHome to the config file's parent
// and runStatus reads the gateway file relative to that anchored home —
// exactly as production looks: ~/.joshbot/config.json next to
// ~/.joshbot/gateway-status.json.
func TestStatusJSON_ReportsLiveGatewayState(t *testing.T) {
	home := t.TempDir()

	// Write the status file as the running gateway would.
	w := gatewaystatus.NewWriter(gatewaystatus.Path(home))
	w.SetPID(777, time.Now().UTC())
	w.SetChannel("telegram", "connected")

	cfg := writeConfigAt(t, home, `{"providers": {"openrouter": {"enabled": true, "api_key": "x"}}}`)
	out, err := runReportCmd(t, runStatus, "--config", cfg, "--output", "json")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, want := range []string{
		`"gateway_running": true`,
		`"gateway_pid": 777`,
		`"name": "telegram"`,
		`"state": "connected"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status --output json missing %s:\n%s", want, out)
		}
	}
}

// TestStatusJSON_GatewayNotRunningWhenNoStatusFile pins the "never guessed"
// contract: with no gateway status file, status reports the gateway as not
// running rather than inventing a live state.
func TestStatusJSON_GatewayNotRunningWhenNoStatusFile(t *testing.T) {
	home := t.TempDir()
	cfg := writeConfigAt(t, home, `{"providers": {"openrouter": {"enabled": true, "api_key": "x"}}}`)
	out, err := runReportCmd(t, runStatus, "--config", cfg, "--output", "json")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"gateway_running": false`) {
		t.Errorf("expected gateway_running=false with no status file:\n%s", out)
	}
	// gateway_pid is omitted (omitempty) when not running.
	if strings.Contains(out, `"gateway_pid"`) {
		t.Errorf("gateway_pid should be omitted when the gateway is not running:\n%s", out)
	}
}

func pollStatusState(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		doc := gatewaystatus.Read(path)
		if doc != nil && len(doc.Channels) == 1 && doc.Channels[0].Name == "telegram" && doc.Channels[0].State == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("telegram state never reached %q; last=%+v", want, doc)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestStartGatewayStatusSink_DisabledWithNoChannel pins that a gateway with
// no Telegram channel records the channel as disabled (never a guessed live
// state), and that stop cancels the polling goroutine (no leaks under -race).
func TestStartGatewayStatusSink_DisabledWithNoChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	path := filepath.Join(t.TempDir(), "gateway-status.json")
	gw := gatewaystatus.NewWriter(path)
	stop := startGatewayStatusSink(ctx, gw, nil)
	defer stop()

	pollStatusState(t, path, "disabled")
}

// TestStartGatewayStatusSink_TracksStateChanges pins the poll-and-write path:
// the initial state is written immediately, and a later channel transition is
// picked up without an explicit write call.
func TestStartGatewayStatusSink_TracksStateChanges(t *testing.T) {
	prevInterval := gatewayStatusPollInterval
	gatewayStatusPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { gatewayStatusPollInterval = prevInterval })

	path := filepath.Join(t.TempDir(), "gateway-status.json")
	gw := gatewaystatus.NewWriter(path)

	var state atomic.Value
	state.Store("reconnecting")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startGatewayStatusSink(ctx, gw, func() string { return state.Load().(string) })
	defer stop()

	pollStatusState(t, path, "reconnecting")

	// Flip the channel state; the sink must pick it up without a write call.
	state.Store("connected")
	pollStatusState(t, path, "connected")

	// Settling on the same value again must not error or flap.
	state.Store("connected")
	pollStatusState(t, path, "connected")
}
