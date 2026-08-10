package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigHome writes a config file into a fresh home and anchors joshbot at
// it, so Load() reads the fixture rather than the developer's real install.
func writeConfigHome(t *testing.T, body string) string {
	t.Helper()
	withHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	SetHome(dir)
	return path
}

func providerFixture(extra string) string {
	return `{
	  "providers": {
	    "openrouter": {"enabled": true` + extra + `}
	  }
	}`
}

const (
	fileKey   = "key-from-file"
	namedKey  = "key-from-named-var"
	canonKey  = "key-from-canonical-var"
	namedVar  = "MY_OPENROUTER_KEY"
	canonVar  = "JOSHBOT_PROVIDERS__OPENROUTER__API_KEY"
	fileField = `, "api_key": "` + fileKey + `"`
	envField  = `, "api_key_env": "` + namedVar + `"`
)

// The documented precedence is JOSHBOT_PROVIDERS__<NAME>__API_KEY > api_key_env
// > api_key. It falls out of ordering rather than a rule, so every reachable
// combination is pinned: a reordering that breaks one of these is silent
// otherwise, and picking the wrong credential surfaces as a 401 the operator
// cannot explain.
func TestLoad_CredentialPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		canonical  string // canonical env var value, "" = unset
		named      string // named (api_key_env) var value, "" = unset
		fields     string // extra JSON fields on the provider
		wantKey    string
		wantSource string
		wantErr    string
	}{
		{
			name:       "api_key alone",
			fields:     fileField,
			wantKey:    fileKey,
			wantSource: CredentialFromFile,
		},
		{
			name:       "api_key_env alone",
			named:      namedKey,
			fields:     envField,
			wantKey:    namedKey,
			wantSource: CredentialFromEnv(namedVar),
		},
		{
			name:       "canonical env alone",
			canonical:  canonKey,
			wantKey:    canonKey,
			wantSource: CredentialFromEnv(canonVar),
		},
		{
			// Both fields set is a conflict, not a precedence question:
			// silently preferring one leaves the operator unable to tell
			// which credential is in use.
			name:    "api_key and api_key_env",
			named:   namedKey,
			fields:  fileField + envField,
			wantErr: "sets both api_key and api_key_env",
		},
		{
			name:       "canonical env beats api_key",
			canonical:  canonKey,
			fields:     fileField,
			wantKey:    canonKey,
			wantSource: CredentialFromEnv(canonVar),
		},
		{
			name:       "canonical env beats api_key_env",
			canonical:  canonKey,
			named:      namedKey,
			fields:     envField,
			wantKey:    canonKey,
			wantSource: CredentialFromEnv(canonVar),
		},
		{
			// The canonical variable is a complete answer, so the named one
			// being unset is redundant rather than a misconfiguration.
			name:       "canonical env covers an unset api_key_env",
			canonical:  canonKey,
			fields:     envField,
			wantKey:    canonKey,
			wantSource: CredentialFromEnv(canonVar),
		},
		{
			name:    "api_key_env names an unset variable",
			fields:  envField,
			wantErr: "which is not set in the environment",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeConfigHome(t, providerFixture(tc.fields))
			if tc.canonical != "" {
				t.Setenv(canonVar, tc.canonical)
			}
			if tc.named != "" {
				t.Setenv(namedVar, tc.named)
			}

			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Load succeeded; want error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				// The operator has to know which provider to go and fix.
				if !strings.Contains(err.Error(), "openrouter") {
					t.Errorf("error = %v, does not name the provider", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Providers["openrouter"].APIKey; got != tc.wantKey {
				t.Errorf("APIKey = %q, want %q", got, tc.wantKey)
			}
			if got := cfg.CredentialSource("openrouter"); got != tc.wantSource {
				t.Errorf("CredentialSource = %q, want %q", got, tc.wantSource)
			}
		})
	}
}

// The variable name is the whole diagnostic: without it a typo in api_key_env
// is indistinguishable from a revoked key, and the operator goes to the
// provider's dashboard instead of their shell profile.
func TestLoad_UnsetAPIKeyEnvNamesTheVariable(t *testing.T) {
	writeConfigHome(t, providerFixture(envField))

	_, err := Load()
	if err == nil {
		t.Fatal("Load succeeded with api_key_env naming an unset variable")
	}
	if !strings.Contains(err.Error(), namedVar) {
		t.Errorf("error = %v, does not name %s", err, namedVar)
	}
}

// Load warns and falls back to defaults on most failures. A credential the
// operator asked for and did not get must not take that path: a default config
// nothing can dial is a worse outcome than a startup error, because the failure
// then appears at the first message rather than at the command that caused it.
func TestLoad_CredentialFailureIsFatalNotDowngraded(t *testing.T) {
	writeConfigHome(t, providerFixture(envField))

	cfg, err := Load()
	if err == nil {
		t.Fatal("Load fell back to defaults instead of reporting the failure")
	}
	if cfg != nil {
		t.Errorf("Load returned a config alongside the error: %+v", cfg)
	}
}

// api_key_env exists so a config file that is backed up, synced or pasted into
// an issue carries a variable name rather than a secret. A resolved credential
// reaching a log file would undo that.
func TestResolvedCredentialDoesNotReachTheConfigFileOrLogs(t *testing.T) {
	path := writeConfigHome(t, providerFixture(envField))
	t.Setenv(namedVar, namedKey)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(saved), namedKey) {
		t.Errorf("Save wrote the resolved credential back into the config file:\n%s", saved)
	}
	if !strings.Contains(string(saved), namedVar) {
		t.Errorf("Save dropped api_key_env, so the indirection is lost on the next write:\n%s", saved)
	}
	// credentialSource is derived state; round-tripping it would put a
	// second copy of the variable name into a serialised field nothing reads.
	if strings.Contains(string(saved), "credentialSource") {
		t.Errorf("Save serialised derived credential-source state:\n%s", saved)
	}
}

// The overwhelming majority of existing installs use a literal api_key and must
// keep working untouched, in both config formats.
func TestLoad_LiteralAPIKeyStillWorks(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		writeConfigHome(t, providerFixture(fileField))
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.Providers["openrouter"].APIKey; got != fileKey {
			t.Errorf("APIKey = %q, want %q", got, fileKey)
		}
		if got := cfg.CredentialSource("openrouter"); got != CredentialFromFile {
			t.Errorf("CredentialSource = %q, want %q", got, CredentialFromFile)
		}
	})

	t.Run("model-centric", func(t *testing.T) {
		writeConfigHome(t, `{
		  "providers": {"openrouter": {"enabled": true, "api_key": "`+fileKey+`"}},
		  "models_config": {
		    "agent": {"model": "openrouter/anthropic/claude-sonnet-4"},
		    "models": [{"name": "openrouter/anthropic/claude-sonnet-4",
		                "model": "openrouter/anthropic/claude-sonnet-4"}]
		  }
		}`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.Providers["openrouter"].APIKey; got != fileKey {
			t.Errorf("APIKey = %q, want %q", got, fileKey)
		}
	})
}

// A provider with neither field configured must report so plainly rather than
// looking like one whose key came from the file.
func TestCredentialSource_Missing(t *testing.T) {
	writeConfigHome(t, providerFixture(""))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.CredentialSource("openrouter"); got != CredentialMissing {
		t.Errorf("CredentialSource = %q, want %q", got, CredentialMissing)
	}
	if got := cfg.CredentialSource("nosuchprovider"); got != CredentialMissing {
		t.Errorf("CredentialSource for an unknown provider = %q, want %q", got, CredentialMissing)
	}
}
