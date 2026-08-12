package configure_test

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/configure"
)

func TestConfigurator_ConfigReturnsSame(t *testing.T) {
	bc := config.Defaults()
	c := configure.New(bc)
	if c.Config() != bc {
		t.Error("Config() must return the same pointer passed to New")
	}
}
