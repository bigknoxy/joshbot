package childenv

import (
	"strings"
	"testing"
)

// TestSanitizedDropsCredentialsKeepsBasics is the package-level pin on the
// screen both the shell tool and the MCP client rely on: a child must be able
// to run a build, and must not be able to read a provider API key.
func TestSanitizedDropsCredentialsKeepsBasics(t *testing.T) {
	t.Setenv("JOSHBOT_PROVIDERS__OPENROUTER__API_KEY", "sk-must-not-leak")
	t.Setenv("OPENAI_API_KEY", "sk-also-must-not-leak")
	t.Setenv("PATH", "/usr/bin")

	env := Sanitized("EXTRA=granted")
	joined := strings.Join(env, "\n")

	for _, leaked := range []string{"JOSHBOT_", "OPENAI_API_KEY"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("sanitized env contains %q:\n%s", leaked, joined)
		}
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Errorf("sanitized env dropped PATH:\n%s", joined)
	}
	if !strings.Contains(joined, "EXTRA=granted") {
		t.Error("explicit extra entry was not appended")
	}
}

func TestIsSecretName(t *testing.T) {
	for _, name := range []string{"OPENAI_API_KEY", "GH_TOKEN", "MY_SECRET", "JOSHBOT_ANYTHING"} {
		if !IsSecretName(name) {
			t.Errorf("IsSecretName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"PATH", "HOME", "KEYBOARD_LAYOUT"} {
		if IsSecretName(name) {
			t.Errorf("IsSecretName(%q) = true, want false", name)
		}
	}
}
