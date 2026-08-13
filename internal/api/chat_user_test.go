package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestUserIsBoundedAndShaped covers what ValidateSessionID does not. The value
// becomes half a session key and therefore a filename, so an unbounded one makes
// every turn for that caller fail to persist, and a name carrying a newline or an
// RTL override renders deceptively in `joshbot sessions` — which is where an
// operator reads it to decide what to prune.
func TestUserIsBoundedAndShaped(t *testing.T) {
	rejected := map[string]string{
		"too long":     strings.Repeat("a", MaxUserLength+1),
		"newline":      "a\nb",
		"space":        "a b",
		"rtl override": "a‮b",
		"tilde":        "~root",
		"colon":        "a:b",
	}
	for name, user := range rejected {
		t.Run("rejected/"+name, func(t *testing.T) {
			a := &fakeAgent{reply: "hi"}
			w := do(t, testServer(t, a), http.MethodPost, "/v1/chat/completions", "secret",
				`{"messages":[{"role":"user","content":"hi"}],"user":`+quote(user)+`}`)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("user %q got %d, want 400", user, w.Code)
			}
			if a.got.SenderID != "" {
				t.Fatalf("user %q reached the agent as %q", user, a.got.SenderID)
			}
		})
	}

	// The accepted shapes are the ones real clients send; rejecting these would
	// make the cap a usability bug rather than a bound.
	for _, user := range []string{"alice", "alice.smith", "user_1", "a-b", strings.Repeat("a", MaxUserLength)} {
		a := &fakeAgent{reply: "hi"}
		w := do(t, testServer(t, a), http.MethodPost, "/v1/chat/completions", "secret",
			`{"messages":[{"role":"user","content":"hi"}],"user":`+quote(user)+`}`)
		if w.Code != http.StatusOK {
			t.Fatalf("user %q got %d, want 200", user, w.Code)
		}
		if a.got.SenderID != user {
			t.Fatalf("user %q reached the agent as %q", user, a.got.SenderID)
		}
	}
}
