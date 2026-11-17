package providers

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// The registered NVIDIA default must survive prefix stripping. The whole
// NVIDIA NIM catalog is "nvidia/...", and stripping the prefix gets a 404
// from the API — so a default that is mangled on the way out is a default
// that cannot make the first call of a fresh install.
func TestNvidiaDefaultModelSurvivesPrefixStripping(t *testing.T) {
	def := GetDefaultModel("nvidia")
	if def == "" {
		t.Fatal("no nvidia default model is registered")
	}
	if got := config.StripProviderPrefix(def); got != def {
		t.Errorf("the registered nvidia default %q is mangled to %q before it is sent, "+
			"so the out-of-the-box configuration cannot work", def, got)
	}
}
