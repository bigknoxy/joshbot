// Package tools provides the tool system for joshbot's agent.
package tools

// filesystemAlias wraps the filesystem tool to provide alternative names
// for operations that LLMs might try to call directly.
type filesystemAlias struct {
	fs   *FilesystemTool
	name string
	op   string
}

// Name returns the alias name.
func (a *filesystemAlias) Name() string {
	return a.name
}

// Description returns the description from the underlying filesystem tool.
func (a *filesystemAlias) Description() string {
	return a.fs.Description()
}

// Parameters returns the parameters from the underlying filesystem tool.
func (a *filesystemAlias) Parameters() []Parameter {
	return a.fs.Parameters()
}

// Execute runs the filesystem tool with the operation already injected.
func (a *filesystemAlias) Execute(ctx interface{}, args map[string]any) ToolResult {
	// Inject the operation parameter so the underlying tool knows what to do
	args["operation"] = a.op
	return a.fs.Execute(ctx, args)
}
