package config

import (
	"errors"
	"strings"
	"testing"
)

// A bad api_url must be fatal, not ordinary: Load answers an ordinary validation
// failure by logging "Config unusable, using defaults" and substituting
// Defaults(), which silently discards every provider, API key and allowlist. A
// typo in one channel key must not cost the operator their whole config (#280,
// same rule as the timeout keys in #240).
func TestValidateTelegramAPIURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"empty means the public Bot API", "", true},
		{"https accepted", "https://tg.example.com", true},
		{"http accepted for a LAN server", "http://192.168.1.9:8081", true},
		{"bare host has no scheme", "tg.example.com:8081", false},
		{"wrong scheme", "ftp://tg.example.com", false},
		{"scheme with no host", "https://", false},
		{"unparseable", "http://[::1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTelegramAPIURL(tc.raw)
			if tc.ok {
				if err != nil {
					t.Fatalf("validateTelegramAPIURL(%q) = %v, want nil", tc.raw, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateTelegramAPIURL(%q) = nil, want an error", tc.raw)
			}
			var fatal fatalConfigError
			if !errors.As(err, &fatal) {
				t.Errorf("error for %q is %T, want a fatalConfigError so Load propagates it", tc.raw, err)
			}
		})
	}
}

// The end-to-end half of the rule above: a config carrying a bad api_url must
// reach the caller as an error, not as Defaults() with the operator's providers
// missing and no error at all.
func TestLoadFileConfigPropagatesBadTelegramAPIURL(t *testing.T) {
	body := []byte(`{"channels":{"telegram":{"enabled":true,"token":"t","api_url":"ftp://nope"}},` +
		`"providers":{"openrouter":{"enabled":true,"api_key":"k"}}}`)

	cfg, err := loadFileConfig(body)
	if err == nil {
		t.Fatalf("loadFileConfig = nil error; a bad api_url would be swallowed (providers: %d)", len(cfg.Providers))
	}
	var fatal fatalConfigError
	if !errors.As(err, &fatal) {
		t.Fatalf("loadFileConfig error is %T, want fatalConfigError so Load does not substitute Defaults()", err)
	}
}

// A valid api_url must survive a serialize/parse round trip, and omitempty must
// keep it out of every config that does not set it — that absence is the whole
// reason the key needs no schema migration.
func TestTelegramAPIURLRoundTripsAndIsOmittedWhenUnset(t *testing.T) {
	data, err := serializeConfig(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "api_url") {
		t.Errorf("unset api_url was serialized; omitempty missing:\n%s", data)
	}

	cfg := Defaults()
	cfg.Channels.Telegram.APIURL = "http://127.0.0.1:8081"
	data, err = serializeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadFileConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Channels.Telegram.APIURL != "http://127.0.0.1:8081" {
		t.Errorf("api_url = %q after round trip, want http://127.0.0.1:8081",
			got.Channels.Telegram.APIURL)
	}
}
