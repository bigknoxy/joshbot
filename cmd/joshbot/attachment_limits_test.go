package main

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
)

// The send_file tool's cap follows the transport it will actually reach, so an
// api_url left in a config with Telegram switched off must not raise it —
// otherwise the tool accepts a 40 MiB send that nothing can deliver, and the
// operator sees a limit they never configured (#280).
func TestOutboundAttachmentLimits(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		apiURL  string
		raised  bool
	}{
		{"telegram off with an api_url", false, "http://127.0.0.1:8081", false},
		{"telegram on, public Bot API", true, "", false},
		{"telegram on, self-hosted", true, "http://127.0.0.1:8081", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Channels.Telegram.Enabled = tc.enabled
			cfg.Channels.Telegram.APIURL = tc.apiURL

			got := outboundAttachmentLimits(cfg)
			raised := got.DocumentMaxBytes > bus.DefaultAttachmentLimits().DocumentMaxBytes
			if raised != tc.raised {
				t.Errorf("outboundAttachmentLimits = %+v (raised=%v), want raised=%v",
					got, raised, tc.raised)
			}
		})
	}
}
