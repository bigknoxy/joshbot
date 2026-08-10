package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/urfave/cli/v2"
)

// --- #139: the gateway bus handler must not write to stdout/stderr directly ---

// TestGatewayHandlerHasNoDirectConsoleWrites guards against reintroducing the
// raw fmt.Fprintf(os.Stderr, ...) debug print that leaked the full text of every
// inbound message. The handler now goes through the redacting logger
// (internal/log wraps its writer with internal/redact), so a direct write is
// both a leak of unredacted content and an unfilterable one — it ignores the
// configured log level.
//
// This is deliberately a SOURCE-LEVEL assertion. A behavioural test cannot
// catch the regression: runGateway's handler is an anonymous closure registered
// on the bus, reachable only by standing up a full gateway (config, providers,
// live channels), and the previous version of this test settled for calling
// log.Debug directly — which asserts the logging library's level filtering, not
// the handler's discipline, and would stay green with a bare
// fmt.Fprintf(os.Stderr, ...) put right back into the handler. Parsing the
// function body is the only assertion that actually fails when the write
// returns.
func TestGatewayHandlerHasNoDirectConsoleWrites(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Recv == nil && f.Name.Name == "runGateway" {
			fn = f
			break
		}
	}
	if fn == nil {
		t.Fatal("runGateway not found in main.go; this guard needs updating")
	}

	// isConsole reports whether an expression names os.Stdout or os.Stderr.
	isConsole := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "os" && (sel.Sel.Name == "Stdout" || sel.Sel.Name == "Stderr")
	}

	var offenders []string
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch {
		// fmt.Fprint*/os.Stderr, io.Copy(os.Stdout, ...), etc: any call whose
		// first argument is the console handle.
		case len(call.Args) > 0 && isConsole(call.Args[0]):
		// os.Stderr.Write(...) / os.Stdout.WriteString(...).
		case isConsole(sel.X):
		default:
			return true
		}
		offenders = append(offenders, fmt.Sprintf("%s: %s", fset.Position(call.Pos()), types.ExprString(call)))
		return true
	})

	if len(offenders) > 0 {
		t.Fatalf("runGateway writes directly to the console, bypassing the redacting logger and the log level:\n  %s\n"+
			"Use log.Debug/log.Info from internal/log instead.", strings.Join(offenders, "\n  "))
	}
}

// The guard above is only meaningful if its detector actually fires, so prove
// it against a fixture containing exactly the regression it screens for.
func TestGatewayConsoleWriteDetectorFires(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"os"
)

func runGateway() {
	fmt.Fprintf(os.Stderr, "DEBUG: %s\n", "leaked")
	os.Stdout.WriteString("also bad")
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	var found int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		isConsole := func(e ast.Expr) bool {
			s, ok := e.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			pkg, ok := s.X.(*ast.Ident)
			return ok && pkg.Name == "os" && (s.Sel.Name == "Stdout" || s.Sel.Name == "Stderr")
		}
		if (len(call.Args) > 0 && isConsole(call.Args[0])) || isConsole(sel.X) {
			found++
		}
		return true
	})
	if found != 2 {
		t.Fatalf("detector found %d console writes in the fixture, want 2", found)
	}
}

// --- #142 / #160: non-interactive onboard flag paths ---

// onboardContext builds a cli.Context for runOnboard with the given flags set.
func onboardContext(t *testing.T, args map[string]string, bools map[string]bool) *cli.Context {
	t.Helper()
	fs := flag.NewFlagSet("onboard", flag.ContinueOnError)
	fs.Bool("force", false, "")
	fs.Bool("keep-data", false, "")
	fs.String("model", "", "")
	fs.String("provider", "", "")
	fs.String("api-key", "", "")
	fs.String("api-base", "", "")
	fs.String("config", "", "") // global --config path flag
	for k, v := range args {
		if err := fs.Set(k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	for k, v := range bools {
		if v {
			if err := fs.Set(k, "true"); err != nil {
				t.Fatalf("set %s: %v", k, err)
			}
		}
	}
	app := cli.NewApp()
	return cli.NewContext(app, fs, nil)
}

// withTempHome points config.DefaultHome/DefaultWorkspace at a temp dir so an
// onboard run never touches the real ~/.joshbot, and restores them after.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	origHome, origWs := config.DefaultHome, config.DefaultWorkspace
	config.DefaultHome = dir
	config.DefaultWorkspace = filepath.Join(dir, "workspace")
	t.Cleanup(func() {
		config.DefaultHome = origHome
		config.DefaultWorkspace = origWs
	})
	return dir
}

// TestOnboardForceNoProviderFails covers #142: `onboard --force </dev/null` with
// nothing to configure must exit non-zero with an actionable message instead of
// printing "Setup complete!" over an unusable config.
func TestOnboardForceNoProviderFails(t *testing.T) {
	withTempHome(t)
	// Ensure no env-provided key silently satisfies the default provider.
	for _, k := range []string{
		"JOSHBOT_PROVIDERS__OPENROUTER__API_KEY", "JOSHBOT_OPENROUTER_API_KEY",
	} {
		t.Setenv(k, "")
	}

	c := onboardContext(t, nil, map[string]bool{"force": true})
	err := runOnboard(c)
	if err == nil {
		t.Fatal("expected error when --force configures no provider, got nil")
	}
	if !strings.Contains(err.Error(), "did not configure any provider") {
		t.Fatalf("error missing actionable guidance: %v", err)
	}
	// The scaffold is still written so a caller that supplies a credential
	// separately gets a usable tree; only the exit status reports the failure.
	if _, statErr := os.Stat(filepath.Join(config.DefaultHome, "config.json")); statErr != nil {
		t.Errorf("config.json should still be written: %v", statErr)
	}
}

// TestOnboardForceWithFlagsSucceeds covers #142/#160 success path: provider and
// key supplied via flags configure a provider non-interactively and validation
// runs against the supplied --api-base (an httptest server), so the run is
// hermetic and exits with no error.
func TestOnboardForceWithFlagsSucceeds(t *testing.T) {
	home := withTempHome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	}))
	defer srv.Close()

	c := onboardContext(t, map[string]string{
		"provider": "openrouter",
		"api-key":  "sk-test-key",
		"api-base": srv.URL,
		"model":    "openrouter/free",
	}, map[string]bool{"force": true})

	if err := runOnboard(c); err != nil {
		t.Fatalf("runOnboard with flags failed: %v", err)
	}

	cfgPath := filepath.Join(home, "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config.json not written: %v", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p, ok := loaded.Providers["openrouter"]
	if !ok {
		t.Fatal("openrouter provider not configured")
	}
	if p.APIKey != "sk-test-key" {
		t.Errorf("APIKey = %q, want sk-test-key", p.APIKey)
	}
	if !p.Enabled {
		t.Error("provider not enabled")
	}
	if p.APIBase != srv.URL {
		t.Errorf("APIBase = %q, want %q", p.APIBase, srv.URL)
	}
	if loaded.ProviderDefaults.Default != "openrouter" {
		t.Errorf("default provider = %q, want openrouter", loaded.ProviderDefaults.Default)
	}
}

// TestOnboardForceReadsEnvKey covers #142: with no --api-key flag, the key is
// read from JOSHBOT_PROVIDERS__<PROVIDER>__API_KEY so an env-driven --force run
// configures a provider instead of failing.
func TestOnboardForceReadsEnvKey(t *testing.T) {
	withTempHome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	t.Setenv("JOSHBOT_PROVIDERS__OPENROUTER__API_KEY", "env-key-123")

	c := onboardContext(t, map[string]string{
		"provider": "openrouter",
		"api-base": srv.URL,
	}, map[string]bool{"force": true})

	if err := runOnboard(c); err != nil {
		t.Fatalf("runOnboard reading env key failed: %v", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Providers["openrouter"].APIKey != "env-key-123" {
		t.Errorf("APIKey = %q, want env-key-123", loaded.Providers["openrouter"].APIKey)
	}
}
