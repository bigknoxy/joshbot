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

// consoleWrites reports every direct console write reachable from root, as
// "position: expression" strings. It recognises three shapes:
//
//   - any call whose first argument is os.Stdout/os.Stderr
//     (fmt.Fprintf(os.Stderr, ...), io.Copy(os.Stdout, ...));
//   - a method on the handle itself (os.Stderr.Write, os.Stdout.WriteString);
//   - fmt.Print/Printf/Println, which write to stdout with no handle named at
//     all — the shape a reintroduced debug print takes most naturally.
//
// It also follows one level of same-file call indirection: a plain call to a
// package-level function declared in file is walked too, so moving the write
// into a helper does not hide it. One level is deliberate — deeper following
// needs real call-graph analysis, and the guard is scoped to a single closure.
//
// fset and file supply positions and the declarations to follow; both tests
// pass their own, so the fixture test exercises this exact detector rather than
// a copy of it.
func consoleWrites(fset *token.FileSet, file *ast.File, root ast.Node) []string {
	// isConsole reports whether an expression names os.Stdout or os.Stderr.
	isConsole := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "os" && (sel.Sel.Name == "Stdout" || sel.Sel.Name == "Stderr")
	}

	// decls indexes the file's package-level functions for indirection.
	decls := map[string]*ast.FuncDecl{}
	if file != nil {
		for _, d := range file.Decls {
			if f, ok := d.(*ast.FuncDecl); ok && f.Recv == nil && f.Body != nil {
				decls[f.Name.Name] = f
			}
		}
	}

	var out []string
	seen := map[string]bool{} // guards recursion and duplicate reports

	var walk func(n ast.Node, depth int)
	walk = func(n ast.Node, depth int) {
		ast.Inspect(n, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			report := func() {
				out = append(out, fmt.Sprintf("%s: %s", fset.Position(call.Pos()), types.ExprString(call)))
			}
			if len(call.Args) > 0 && isConsole(call.Args[0]) {
				report()
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				// os.Stderr.Write(...) / os.Stdout.WriteString(...).
				if isConsole(fn.X) {
					report()
					return true
				}
				// fmt.Print/Printf/Println default to stdout.
				if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "fmt" &&
					(fn.Sel.Name == "Print" || fn.Sel.Name == "Printf" || fn.Sel.Name == "Println") {
					report()
					return true
				}
			case *ast.Ident:
				// One level of same-file indirection: helper() that writes.
				if depth > 0 {
					return true
				}
				target, ok := decls[fn.Name]
				if !ok || seen[fn.Name] {
					return true
				}
				seen[fn.Name] = true
				walk(target.Body, depth+1)
			}
			return true
		})
	}
	walk(root, 0)
	return out
}

// gatewayBusHandler returns the closure runGateway registers on the message bus
// (msgBus.Subscribe("all", func(...){...})). The guard is scoped to that
// closure rather than to all of runGateway on purpose: runGateway legitimately
// prints a startup banner with fmt.Println, so a whole-function assertion would
// have to tolerate fmt.Print* and would then miss a reintroduced
// fmt.Printf("DEBUG: %s", msg.Content) inside the handler — which is the actual
// regression.
func gatewayBusHandler(fn *ast.FuncDecl) *ast.FuncLit {
	var lit *ast.FuncLit
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || lit != nil {
			return lit == nil
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Subscribe" {
			return true
		}
		for _, arg := range call.Args {
			if f, ok := arg.(*ast.FuncLit); ok {
				lit = f
				return false
			}
		}
		return true
	})
	return lit
}

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

	handler := gatewayBusHandler(fn)
	if handler == nil {
		t.Fatal("runGateway no longer registers a bus handler closure via Subscribe; this guard needs updating")
	}

	if offenders := consoleWrites(fset, file, handler.Body); len(offenders) > 0 {
		t.Fatalf("the gateway bus handler writes directly to the console, bypassing the redacting logger and the log level:\n  %s\n"+
			"Use log.Debug/log.Info from internal/log instead.", strings.Join(offenders, "\n  "))
	}
}

// The guard above is only meaningful if its detector actually fires, so prove
// it against a fixture containing exactly the regressions it screens for —
// including the fmt.Printf form and a write hidden one call deep. This invokes
// the real consoleWrites and gatewayBusHandler, so breaking either turns this
// test red rather than leaving a re-implemented copy passing.
func TestGatewayConsoleWriteDetectorFires(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"os"
)

func leakHelper(msg string) {
	fmt.Fprintf(os.Stderr, "DEBUG: %s\n", msg)
}

func runGateway() {
	fmt.Println("joshbot gateway starting")   // legitimate banner, outside the handler
	msgBus.Subscribe("all", func(msg string) {
		fmt.Fprintf(os.Stderr, "DEBUG: %s\n", msg)
		os.Stdout.WriteString("also bad")
		fmt.Printf("DEBUG: %s\n", msg)
		leakHelper(msg)
	})
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "runGateway" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("fixture runGateway not found")
	}
	handler := gatewayBusHandler(fn)
	if handler == nil {
		t.Fatal("gatewayBusHandler did not find the Subscribe closure in the fixture")
	}

	got := consoleWrites(fset, file, handler.Body)
	if len(got) != 4 {
		t.Fatalf("detector found %d console writes in the handler, want 4:\n  %s", len(got), strings.Join(got, "\n  "))
	}
	for _, want := range []string{"fmt.Fprintf", "os.Stdout.WriteString", "fmt.Printf"} {
		found := false
		for _, g := range got {
			if strings.Contains(g, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("detector missed the %s form:\n  %s", want, strings.Join(got, "\n  "))
		}
	}

	// The banner outside the handler must NOT be reported: the guard is scoped
	// to the closure, so legitimate startup output stays legal.
	for _, g := range got {
		if strings.Contains(g, "joshbot gateway starting") {
			t.Errorf("detector flagged the legitimate banner print: %s", g)
		}
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
