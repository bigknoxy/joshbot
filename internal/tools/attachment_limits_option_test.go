package tools

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
)

func sendFileToolFrom(t *testing.T, reg *Registry) *SendFileTool {
	t.Helper()
	tool, ok := reg.Get("send_file")
	if !ok {
		t.Fatal("registry has no send_file tool")
	}
	sf, ok := tool.(*SendFileTool)
	if !ok {
		t.Fatalf("send_file tool is %T, want *SendFileTool", tool)
	}
	return sf
}

// send_file and the Telegram channel enforce the outbound cap independently —
// the bus is a public boundary — so a raise that reaches only the transport
// leaves the tool refusing sends the transport would have accepted, and the
// operator sees a limit they did not configure. This option is the only wiring
// that keeps the two in step (#280).
func TestWithAttachmentLimitsReachesSendFileTool(t *testing.T) {
	ws := t.TempDir()
	sender := &mockSender{}

	t.Run("absent option leaves the defaults", func(t *testing.T) {
		sf := sendFileToolFrom(t, RegistryWithDefaults(ws, true, 5, 5, sender, nil, nil, nil))
		if got := sf.Limits(); got != bus.DefaultAttachmentLimits() {
			t.Errorf("Limits() = %+v, want the bus defaults %+v", got, bus.DefaultAttachmentLimits())
		}
	})

	t.Run("option is applied", func(t *testing.T) {
		want := bus.AttachmentLimits{PhotoMaxBytes: 50 << 20, DocumentMaxBytes: 50 << 20}
		sf := sendFileToolFrom(t, RegistryWithDefaults(ws, true, 5, 5, sender, nil, nil, nil,
			WithAttachmentLimits(want)))
		if got := sf.Limits(); got != want {
			t.Errorf("Limits() = %+v, want %+v", got, want)
		}
	})
}
