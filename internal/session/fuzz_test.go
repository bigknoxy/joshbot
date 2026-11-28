package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzSessionIDCannotEscapeTheSessionsDirectory is the property the three
// CodeQL go/path-injection alerts on this file depend on.
//
// The session id is "channel:senderID" and the sender part comes from an
// external system, so it is attacker-influenced. ValidateSessionID rejects the
// empty string, a null byte, a path separator and any "..", and every path
// this package builds is downstream of that check -- but a hand-written
// validator is exactly the kind of thing a future change can quietly weaken.
// A reviewer reading the validator can see the rule; a fuzzer tries to break
// it.
//
// The property: for every id the validator accepts, every path the manager
// builds from it stays inside the sessions directory, and the id itself is
// recoverable from the file name.
func FuzzSessionIDCannotEscapeTheSessionsDirectory(f *testing.F) {
	// The shapes that matter: the separator, the parent reference, the null
	// byte, the empty string, and a plain id.
	f.Add("cli:user@example.com")
	f.Add("cli:../../etc/passwd")
	f.Add("cli:a/b")
	f.Add(`cli:a\b`)
	f.Add("cli:..")
	f.Add("cli:.")
	f.Add("cli:")
	f.Add("")
	f.Add("cli:ok")
	f.Add("cli:a..b")

	m := &Manager{sessionsDir: filepath.Join("/sessions", "root")}
	root, err := filepath.Abs(m.sessionsDir)
	if err != nil {
		f.Fatalf("abs: %v", err)
	}

	f.Fuzz(func(t *testing.T, id string) {
		if err := ValidateSessionID(id); err != nil {
			// Rejected ids never reach a path. That is the point of the
			// validator, and the fuzzer's job is to make sure the accepted
			// ones are the only ones that can.
			return
		}

		for _, path := range []string{
			m.sessionFilePath(id),
			m.metadataFilePath(id),
			m.quarantineFilePath(id),
			m.logFilePath(id),
		} {
			clean := filepath.Clean(path)
			if !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
				t.Fatalf("session id %q escaped the sessions directory: %q", id, clean)
			}
			if strings.ContainsRune(id, os.PathSeparator) {
				t.Fatalf("accepted session id %q contains a path separator", id)
			}
		}

		// The id must also survive a round trip through the file name: a
		// session written under one id has to be found under that same id.
		base := filepath.Base(m.sessionFilePath(id))
		if !strings.HasPrefix(base, id) {
			t.Fatalf("session id %q is not recoverable from the file name %q", id, base)
		}
	})
}
