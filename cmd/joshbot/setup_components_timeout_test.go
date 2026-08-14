package main

import (
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/config"
)

// agentFrom runs setupComponents and returns the agent it built — the same
// object every channel dials.
func agentFrom(t *testing.T, cfg *config.Config) *agent.Agent {
	t.Helper()
	// setupComponents leaves background services running; stop them so their
	// goroutines do not outlive the test (see setupConfig).
	t.Cleanup(stopBackgroundServices)

	_, _, _, a, _, _, err := setupComponents(cfg)
	if err != nil {
		t.Fatalf("setupComponents: %v", err)
	}
	return a
}

// TestConfiguredAgentTimeoutReachesTheAgent covers the one line that makes
// agents.defaults.timeout do anything: agent.WithTimeout in setupComponents.
// The agent-package tests prove WithTimeout works and that a zero is ignored,
// but both still pass with that call deleted — the key would then be readable,
// documented, validated and inert, which is exactly the shape of the #241 bug
// it was added to fix.
func TestConfiguredAgentTimeoutReachesTheAgent(t *testing.T) {
	cfg := config.Defaults()
	cfg.Providers["openrouter"] = config.ProviderConfig{
		Enabled: true,
		APIKey:  "sk-test",
	}

	if got := agentFrom(t, cfg).Timeout(); got != agent.DefaultTimeout {
		t.Fatalf("unset agents.defaults.timeout gave the agent %v, want DefaultTimeout %v", got, agent.DefaultTimeout)
	}

	cfg.Agents.Defaults.Timeout = config.Duration(17 * time.Minute)
	if got := agentFrom(t, cfg).Timeout(); got != 17*time.Minute {
		t.Fatalf("agents.defaults.timeout of 17m gave the agent %v", got)
	}
}
