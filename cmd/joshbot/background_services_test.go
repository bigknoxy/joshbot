package main

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// setupComponents starts cron, the heartbeat and the memory consolidator, none
// of which the caller receives a handle to. The consolidator runs its first
// pass immediately from a goroutine and writes into workspace/memory, so a
// setup nobody can stop keeps writing after its caller is finished: in tests
// that races t.TempDir removal ("directory not empty" — the failure that took
// main CI red on ac13b2f), and in production it runs on through shutdown.
func TestSetupComponentsRegistersStoppableBackgroundServices(t *testing.T) {
	stopBackgroundServices() // start from a clean registry

	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test", APIBase: "https://example.invalid/v1"},
	}
	if _, _, _, _, _, _, err := setupComponents(cfg); err != nil {
		t.Fatalf("setupComponents: %v", err)
	}

	bgMu.Lock()
	got := len(bgStoppers)
	bgMu.Unlock()
	// cron, heartbeat, consolidator: every service started in setupComponents
	// must be registered, or it is the one still writing during cleanup.
	if got < 3 {
		t.Fatalf("setupComponents registered %d background services, want at least 3 (cron, heartbeat, consolidator)", got)
	}

	stopBackgroundServices()
	bgMu.Lock()
	left := len(bgStoppers)
	bgMu.Unlock()
	if left != 0 {
		t.Fatalf("stopBackgroundServices left %d stoppers registered", left)
	}

	// Every stopper is one-shot — heartbeat.Stop and Consolidator.Stop close a
	// channel — so a second call must be a no-op rather than a panic. It is
	// deferred next to closeMCPServers on paths that can also stop explicitly.
	stopBackgroundServices()
}
