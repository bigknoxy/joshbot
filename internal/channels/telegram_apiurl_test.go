package channels

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// createBot dials t.apiURL, so a config value that never reaches the field
// means channels.telegram.api_url is accepted, documented and silently ignored
// — joshbot keeps talking to api.telegram.org (#280).
func TestNewTelegramChannelCarriesAPIURL(t *testing.T) {
	cfg := config.TelegramConfig{Token: "t", APIURL: "http://127.0.0.1:8081"}
	tg := NewTelegramChannel(nil, &cfg)
	if tg.apiURL != "http://127.0.0.1:8081" {
		t.Fatalf("apiURL = %q, want the configured api_url", tg.apiURL)
	}
	if got := tg.AttachmentLimits(); got.DocumentMaxBytes != LocalAPIDocumentMaxBytes {
		t.Errorf("DocumentMaxBytes = %d, want the raised %d", got.DocumentMaxBytes, LocalAPIDocumentMaxBytes)
	}

	plain := NewTelegramChannel(nil, &config.TelegramConfig{Token: "t"})
	if plain.apiURL != "" {
		t.Errorf("apiURL = %q with no api_url set, want empty", plain.apiURL)
	}
	if got := plain.AttachmentLimits(); got != bus.DefaultAttachmentLimits() {
		t.Errorf("AttachmentLimits() = %+v with no api_url, want the defaults %+v",
			got, bus.DefaultAttachmentLimits())
	}
}

// One rule, two callers: the channel enforces it here and send_file enforces it
// through cmd/joshbot. A second copy would drift, and the symptom is a tool
// refusing a send the transport would have accepted.
func TestTelegramAttachmentLimitsFor(t *testing.T) {
	if got := TelegramAttachmentLimitsFor(""); got != bus.DefaultAttachmentLimits() {
		t.Errorf("empty api_url = %+v, want the public Bot API defaults %+v",
			got, bus.DefaultAttachmentLimits())
	}
	got := TelegramAttachmentLimitsFor("http://127.0.0.1:8081")
	if got.PhotoMaxBytes != LocalAPIPhotoMaxBytes || got.DocumentMaxBytes != LocalAPIDocumentMaxBytes {
		t.Errorf("local api_url = %+v, want photo=%d document=%d",
			got, LocalAPIPhotoMaxBytes, LocalAPIDocumentMaxBytes)
	}
	// The cap is a memory bound: the whole payload is held from the tool call
	// until the upload finishes. Raising it past what one turn may hold in heap
	// needs the fd-carrying rework in #305, not a bigger constant.
	if LocalAPIDocumentMaxBytes != 50<<20 {
		t.Errorf("LocalAPIDocumentMaxBytes = %d, want 50 MiB", LocalAPIDocumentMaxBytes)
	}
}

// Raising the outbound transport cap must not move any inbound limit: those
// bound what joshbot downloads and forwards to a provider, which a self-hosted
// Bot API server does not change.
func TestLocalAPIRaiseDoesNotTouchInboundLimits(t *testing.T) {
	if providers.MaxImageBytes != 5<<20 {
		t.Errorf("MaxImageBytes = %d, want 5 MiB", providers.MaxImageBytes)
	}
	if providers.MaxDocumentBytes != 8<<20 {
		t.Errorf("MaxDocumentBytes = %d, want 8 MiB", providers.MaxDocumentBytes)
	}
}
