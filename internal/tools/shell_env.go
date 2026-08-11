package tools

import "github.com/bigknoxy/joshbot/internal/childenv"

// The allowlist and the credential screen that decide what a spawned command
// inherits live in internal/childenv, because MCP server processes need the
// exact same treatment and internal/tools imports internal/mcp — a screen kept
// here would have to be duplicated there. These aliases keep the shell tool's
// call sites reading as before.
var (
	isSecretEnvName = childenv.IsSecretName
	sanitizedEnv    = childenv.Sanitized
)
