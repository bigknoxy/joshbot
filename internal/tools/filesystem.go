package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ContextKey is a type for context keys.
type ContextKey string

const (
	// ContextKeyWorkspace is the context key for the workspace directory.
	ContextKeyWorkspace ContextKey = "workspace"
	// ContextKeyLogger is the context key for the logger.
	ContextKeyLogger ContextKey = "logger"
)

// FilesystemTool provides file system operations.
type FilesystemTool struct {
	workspace string
	restrict  bool
}

// NewFilesystemTool creates a new FilesystemTool.
func NewFilesystemTool(workspace string, restrict bool) *FilesystemTool {
	return &FilesystemTool{
		workspace: workspace,
		restrict:  restrict,
	}
}

// Name returns the name of the tool.
func (t *FilesystemTool) Name() string {
	return "filesystem"
}

// Description returns a description of the tool.
func (t *FilesystemTool) Description() string {
	return `Filesystem operations including reading, writing, editing, listing, and searching files. ` +
		`Use this tool to interact with files and directories in the workspace.`
}

// Parameters returns the parameters for the tool.
func (t *FilesystemTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "operation",
			Type:        ParamString,
			Description: "The operation to perform: read_file, write_file, edit_file, list_dir, glob, grep",
			Required:    true,
			Enum:        []string{"read_file", "write_file", "edit_file", "list_dir", "glob", "grep"},
		},
		{
			Name:        "path",
			Type:        ParamString,
			Description: "The file or directory path (relative to workspace if restricting)",
			Required:    false,
		},
		{
			Name:        "content",
			Type:        ParamString,
			Description: "Content to write (for write_file operation)",
			Required:    false,
		},
		{
			Name:        "search",
			Type:        ParamString,
			Description: "Search pattern (for grep or edit_file)",
			Required:    false,
		},
		{
			Name:        "replace",
			Type:        ParamString,
			Description: "Replacement text (for edit_file)",
			Required:    false,
		},
		{
			Name:        "pattern",
			Type:        ParamString,
			Description: "Glob pattern (for glob operation)",
			Required:    false,
		},
		{
			Name:        "offset",
			Type:        ParamInteger,
			Description: "Line offset to start reading from (for read_file, 0-indexed)",
			Required:    false,
			Default:     0,
		},
		{
			Name:        "limit",
			Type:        ParamInteger,
			Description: "Number of lines to read (for read_file)",
			Required:    false,
			Default:     100,
		},
	}
}

// Execute runs the filesystem operation.
func (t *FilesystemTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	// Extract context values
	var workspace string

	if ctx != nil {
		if c, ok := ctx.(context.Context); ok {
			if w := c.Value(ContextKeyWorkspace); w != nil {
				workspace, _ = w.(string)
			}
		}
	}

	// Fall back to configured workspace
	if workspace == "" {
		workspace = t.workspace
	}

	operation, _ := args["operation"].(string)
	path, _ := args["path"].(string)

	// Resolve path with workspace restriction
	resolvedPath, err := t.resolvePath(workspace, path)
	if err != nil {
		return ToolResult{Error: err}
	}

	switch operation {
	case "read_file":
		return t.readFile(resolvedPath, args)
	case "write_file":
		return t.writeFile(resolvedPath, args)
	case "edit_file":
		return t.editFile(resolvedPath, args)
	case "list_dir":
		return t.listDir(resolvedPath)
	case "glob":
		return t.glob(workspace, args)
	case "grep":
		return t.grep(resolvedPath, args)
	default:
		return ToolResult{Error: fmt.Errorf("unknown operation: %s", operation)}
	}
}

// resolvePath resolves a path with workspace restriction.
func (t *FilesystemTool) resolvePath(workspace, path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}

	// If path is absolute, check restrictions
	if filepath.IsAbs(path) {
		if t.restrict && !strings.HasPrefix(path, workspace) {
			return "", fmt.Errorf("access denied: path %s is outside workspace %s", path, workspace)
		}
		return filepath.Clean(path), nil
	}

	// Resolve relative path
	resolved := filepath.Join(workspace, path)
	cleaned := filepath.Clean(resolved)

	// Check workspace restriction
	if t.restrict && !strings.HasPrefix(cleaned, workspace) {
		return "", fmt.Errorf("access denied: path %s is outside workspace %s", path, workspace)
	}

	return cleaned, nil
}

// readFile reads a file's contents.
func (t *FilesystemTool) readFile(path string, args map[string]any) ToolResult {
	offset := 0
	limit := 100

	if o, ok := args["offset"].(float64); ok {
		offset = int(o)
	}
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to read file: %w", err)}
	}

	lines := strings.Split(string(data), "\n")

	// Apply offset and limit
	if offset >= len(lines) {
		return ToolResult{Output: "(empty - offset beyond file length)"}
	}

	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}

	selectedLines := lines[offset:end]

	// Add context info
	output := fmt.Sprintf("File: %s (lines %d-%d of %d)\n", path, offset+1, end, len(lines))
	output += strings.Join(selectedLines, "\n")

	return ToolResult{Output: output}
}

// writeFile writes content to a file.
func (t *FilesystemTool) writeFile(path string, args map[string]any) ToolResult {
	content, _ := args["content"].(string)

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to create directory: %w", err)}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to write file: %w", err)}
	}

	return ToolResult{Output: fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path)}
}

// editFile performs search and replace on a file.
func (t *FilesystemTool) editFile(path string, args map[string]any) ToolResult {
	search, _ := args["search"].(string)
	replace, _ := args["replace"].(string)

	if search == "" {
		return ToolResult{Error: errors.New("search pattern is required")}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to read file: %w", err)}
	}

	content := string(data)
	modified := strings.Replace(content, search, replace, 1)

	if content == modified {
		return ToolResult{Error: errors.New("search pattern not found in file")}
	}

	if err := os.WriteFile(path, []byte(modified), 0o644); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to write file: %w", err)}
	}

	return ToolResult{Output: fmt.Sprintf("Successfully edited %s", path)}
}

// listDir lists directory contents.
func (t *FilesystemTool) listDir(path string) ToolResult {
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ToolResult{Error: fmt.Errorf("directory does not exist: %s", path)}
		}
		return ToolResult{Error: fmt.Errorf("failed to read directory: %w", err)}
	}

	var output strings.Builder
	output.WriteString(fmt.Sprintf("Contents of %s:\n", path))

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		typeChar := "-"
		if entry.IsDir() {
			typeChar = "d"
		}

		output.WriteString(fmt.Sprintf("  %s %10d %s\n", typeChar, info.Size(), entry.Name()))
	}

	return ToolResult{Output: output.String()}
}

// glob finds files matching a pattern.
func (t *FilesystemTool) glob(workspace string, args map[string]any) ToolResult {
	pattern, _ := args["pattern"].(string)

	if pattern == "" {
		return ToolResult{Error: errors.New("pattern is required")}
	}

	// Resolve pattern relative to workspace
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(workspace, pattern)
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("invalid pattern: %w", err)}
	}

	if len(matches) == 0 {
		return ToolResult{Output: "No files match the pattern"}
	}

	output := fmt.Sprintf("Found %d files matching %s:\n", len(matches), args["pattern"])
	for _, match := range matches {
		// Make paths relative to workspace
		rel, err := filepath.Rel(workspace, match)
		if err != nil {
			rel = match
		}
		output += "  " + rel + "\n"
	}

	return ToolResult{Output: output}
}

// grep searches file contents.
func (t *FilesystemTool) grep(workspace string, args map[string]any) ToolResult {
	pattern, _ := args["search"].(string)
	path, _ := args["path"].(string)

	if pattern == "" {
		return ToolResult{Error: errors.New("search pattern is required")}
	}

	// If path is a file, search just that file
	// If path is a directory, search all files in it
	// If path is empty, search the entire workspace
	searchPath := workspace
	if path != "" {
		searchPath = filepath.Join(workspace, path)
	}

	var matches []string
	var filesSearched int

	err := filepath.Walk(searchPath, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Skip common non-text files
		ext := strings.ToLower(filepath.Ext(p))
		skipExts := []string{".png", ".jpg", ".jpeg", ".gif", ".pdf", ".zip", ".tar", ".gz", ".exe", ".so", ".dll"}
		for _, skip := range skipExts {
			if ext == skip {
				return nil
			}
		}

		filesSearched++

		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}

		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(line, pattern) {
				rel, _ := filepath.Rel(workspace, p)
				matches = append(matches, fmt.Sprintf("%s:%d: %s", rel, i+1, line))
			}
		}

		return nil
	})

	if err != nil {
		return ToolResult{Error: fmt.Errorf("search failed: %w", err)}
	}

	if len(matches) == 0 {
		return ToolResult{Output: fmt.Sprintf("No matches found (searched %d files)", filesSearched)}
	}

	output := fmt.Sprintf("Found %d matches in %d files:\n", len(matches), filesSearched)
	output += strings.Join(matches[:min(100, len(matches))], "\n")

	if len(matches) > 100 {
		output += fmt.Sprintf("\n... and %d more", len(matches)-100)
	}

	return ToolResult{Output: output}
}

// FilesystemToolConfig holds configuration for the filesystem tool.
type FilesystemToolConfig struct {
	Workspace string
	Restrict  bool
}

// NewFilesystemToolFromConfig creates a FilesystemTool from config.
func NewFilesystemToolFromConfig(cfg FilesystemToolConfig) *FilesystemTool {
	workspace := cfg.Workspace
	if workspace == "" {
		workspace = os.Getenv("JOSHBOT_WORKSPACE")
		if workspace == "" {
			workspace = filepath.Join(os.Getenv("HOME"), ".joshbot", "workspace")
		}
	}
	return NewFilesystemTool(workspace, cfg.Restrict)
}
