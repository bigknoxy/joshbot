package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestCLICommandNamesAllHaveHandlers pins the TUI Tab-completion list against
// the agent's actual command dispatch. The list carried "clear" and "history"
// for a buffered prompt that never implemented them, so Tab completed a command
// the agent then answered as ordinary prose — a silent failure with no error
// anywhere. Anything offered for completion must be dispatched somewhere.
//
// The agent's switch is parsed out of the source rather than exercised, because
// handleCommand is unexported and constructing an Agent with a session manager,
// provider and tool registry to assert on a string list would test the fixture.
func TestCLICommandNamesAllHaveHandlers(t *testing.T) {
	agentCmds := agentCommandCases(t)

	// Handled by runAgentLoop itself, never forwarded to the agent.
	loopHandled := map[string]bool{"exit": true}

	for _, name := range cliCommandNames {
		if loopHandled[name] {
			continue
		}
		if !agentCmds[name] {
			t.Errorf("cliCommandNames offers /%s for Tab completion but the agent has no case for it — it will be answered as prose", name)
		}
	}
}

// TestCLICommandNamesHasNoDuplicates guards the completion menu against showing
// the same entry twice.
func TestCLICommandNamesHasNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range cliCommandNames {
		if seen[name] {
			t.Errorf("cliCommandNames lists %q twice", name)
		}
		seen[name] = true
	}
}

// agentCommandCases extracts the case literals of handleCommand's switch on
// cmd from internal/agent/agent.go.
func agentCommandCases(t *testing.T) map[string]bool {
	t.Helper()
	const src = "../../internal/agent/agent.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handleCommand" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			tag, ok := sw.Tag.(*ast.Ident)
			if !ok || tag.Name != "cmd" {
				return true
			}
			for _, stmt := range sw.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range clause.List {
					lit, ok := expr.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err == nil {
						found[v] = true
					}
				}
			}
			return true
		})
	}
	if len(found) == 0 {
		t.Fatalf("no command cases found in %s — handleCommand's switch on cmd was not located", src)
	}
	return found
}
