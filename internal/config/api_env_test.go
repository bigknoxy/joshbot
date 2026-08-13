package config

import "testing"

// TestAPIKeysFromEnv pins a promise made in an error message: api.New tells the
// operator to set JOSHBOT_API__API_KEYS, and this package has no viper
// AutomaticEnv — every override is hand-written, so an unwritten one makes the
// advice a lie. It replaces the configured list rather than adding to it, so an
// operator can revoke a key that is still in the file.
func TestAPIKeysFromEnv(t *testing.T) {
	t.Setenv("JOSHBOT_API__API_KEYS", " k1 ,k2,,")
	t.Setenv("JOSHBOT_API__LISTEN", "0.0.0.0:9999")
	cfg := Defaults()
	cfg.API.APIKeys = []string{"from-file"}
	applyEnvOverrides(cfg)

	if cfg.API.Listen != "0.0.0.0:9999" {
		t.Fatalf("listen %q", cfg.API.Listen)
	}
	if len(cfg.API.APIKeys) != 2 || cfg.API.APIKeys[0] != "k1" || cfg.API.APIKeys[1] != "k2" {
		t.Fatalf("keys %q, want [k1 k2] with blanks dropped and the file key replaced", cfg.API.APIKeys)
	}
}

// TestAPIDefaultsToLoopback pins the bind default. This endpoint reaches the
// shell and filesystem tools, so a default of ":port" would publish them to the
// local network the first time anyone runs joshbot serve.
func TestAPIDefaultsToLoopback(t *testing.T) {
	if got := Defaults().API.Listen; got != DefaultAPIListen {
		t.Fatalf("default listen %q, want %q", got, DefaultAPIListen)
	}
	// Spelled out rather than compared to the constant alone: the point of the
	// test is the loopback host, which a rename of the constant would not catch.
	if DefaultAPIListen != "127.0.0.1:18791" {
		t.Fatalf("DefaultAPIListen %q is not loopback", DefaultAPIListen)
	}
}
