package tools

import (
	"os"
	"strings"
)

// Environment handling for spawned shell commands.
//
// A child process inherits the parent's environment unless told otherwise, so
// without this every command the model runs receives joshbot's own provider
// API keys. That is a credential leak no filesystem sandbox can close: the
// secret is already in the child's environment, and `env` is enough to read
// it — nothing has to be fetched from disk, and no deny-list pattern is
// involved.
//
// The default is an allowlist rather than a deny-list. A deny-list that misses
// one variable leaks a credential, and we cannot enumerate the variables a
// given user happens to have exported. The allowlist is then screened a second
// time by isSecretEnvName, so a secret-shaped name cannot ride in on a broad
// prefix rule (GOOGLE_API_KEY, for instance, matches a "GO" prefix).

// shellEnvAllowlist holds variables a build, test or git command legitimately
// needs. Stripping the environment to nothing would close the leak by making
// the tool useless.
var shellEnvAllowlist = map[string]bool{
	// Core process environment.
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"PWD": true, "TERM": true, "TZ": true, "TMPDIR": true, "LANG": true,
	"LANGUAGE": true, "HOSTNAME": true,

	// Go.
	"GOPATH": true, "GOROOT": true, "GOCACHE": true, "GOMODCACHE": true,
	"GOFLAGS": true, "GOBIN": true, "GOOS": true, "GOARCH": true,
	"GOTMPDIR": true, "GOWORK": true, "GOPRIVATE": true, "GONOSUMDB": true,
	"CGO_ENABLED": true,

	// Other common toolchains.
	"CARGO_HOME": true, "RUSTUP_HOME": true, "JAVA_HOME": true,
	"MAVEN_HOME": true, "GRADLE_HOME": true, "NODE_PATH": true,
	"NVM_DIR": true, "PNPM_HOME": true, "PYENV_ROOT": true,
	"PYTHONPATH": true, "VIRTUAL_ENV": true, "CONDA_PREFIX": true,
	"RBENV_ROOT": true, "GEM_HOME": true, "DOTNET_ROOT": true,
}

// shellEnvAllowPrefixes are families that are safe as a group. Kept short on
// purpose — every prefix here widens what reaches the child.
var shellEnvAllowPrefixes = []string{
	"LC_",  // locale
	"XDG_", // base directory spec
}

// secretNameFragments mark a variable as credential-shaped. Matched
// case-insensitively as substrings. These are deliberately specific:
// a bare "KEY" would reject KEYBOARD_LAYOUT and similar.
var secretNameFragments = []string{
	"API_KEY", "APIKEY", "ACCESS_KEY", "SECRET_KEY", "PRIVATE_KEY",
	"SESSION_KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD",
	"CREDENTIAL", "WEBHOOK", "AUTH", "BEARER", "PASSPHRASE",
}

// isSecretEnvName reports whether a variable name looks like it carries a
// credential. Used as a second gate over the allowlist, not as the primary
// control — a deny-list alone would be exactly the pattern-screening approach
// this package already documents as insufficient elsewhere.
func isSecretEnvName(name string) bool {
	upper := strings.ToUpper(name)

	// Everything joshbot itself exports is configuration, including keys.
	if strings.HasPrefix(upper, "JOSHBOT_") {
		return true
	}
	for _, frag := range secretNameFragments {
		if strings.Contains(upper, frag) {
			return true
		}
	}
	return false
}

// allowedEnvName reports whether a variable may be passed to a spawned command.
func allowedEnvName(name string) bool {
	if isSecretEnvName(name) {
		return false
	}
	if shellEnvAllowlist[name] {
		return true
	}
	for _, prefix := range shellEnvAllowPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// sanitizedEnv returns the environment to hand a spawned command: the parent's
// environment reduced to the allowlist, with anything credential-shaped
// removed. Extra entries are appended verbatim for callers that need to inject
// something specific.
func sanitizedEnv(extra ...string) []string {
	parent := os.Environ()
	out := make([]string, 0, len(parent))

	for _, kv := range parent {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if allowedEnvName(name) {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}
