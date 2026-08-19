package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ContextKey is a type for context keys.
type ContextKey string

const (
	// ContextKeyWorkspace is the context key for the workspace directory.
	ContextKeyWorkspace ContextKey = "workspace"
	// ContextKeyLogger is the context key for the logger.
	ContextKeyLogger ContextKey = "logger"
	// ContextKeyChannel is the context key for the channel the current turn
	// arrived on. It rides the request context, the way WithApprover does, so
	// concurrent turns on different channels cannot cross-deliver: a struct
	// field would be one shared slot for every in-flight turn.
	ContextKeyChannel ContextKey = "channel"
)

// WithChannel attaches the current turn's channel to the request context. It is
// what lets send_file resolve a recipient without taking an address from the
// model — the recipient is a property of the turn, not an argument.
func WithChannel(ctx context.Context, name string) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, ContextKeyChannel, name)
}

// ChannelFromContext returns the turn's channel, or "" when there is none. An
// empty result is a refusal at the call site, never a default: a wrong
// recipient is silent, and a failure is not.
func ChannelFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	name, _ := ctx.Value(ContextKeyChannel).(string)
	return name
}

// FilesystemTool provides file system operations.
type FilesystemTool struct {
	workspace      string
	restrict       bool
	allowedPaths   []string // Additional paths allowed outside workspace
	maxOutputChars int      // Maximum characters to truncate file reads to
}

// NewFilesystemTool creates a new FilesystemTool.
func NewFilesystemTool(workspace string, restrict bool, allowedPaths ...string) *FilesystemTool {
	return NewFilesystemToolWithMaxOutput(workspace, restrict, 8000, allowedPaths...)
}

// NewFilesystemToolWithMaxOutput creates a new FilesystemTool with custom max output chars.
func NewFilesystemToolWithMaxOutput(workspace string, restrict bool, maxOutputChars int, allowedPaths ...string) *FilesystemTool {
	return &FilesystemTool{
		workspace:      workspace,
		restrict:       restrict,
		allowedPaths:   allowedPaths,
		maxOutputChars: maxOutputChars,
	}
}

// Name returns the name of the tool.
func (t *FilesystemTool) Name() string {
	return "filesystem"
}

// Description returns a description of the tool.
func (t *FilesystemTool) Description() string {
	return `filesystem: read, write, edit, list, search files and directories.`
}

// Parameters returns the parameters for the tool.
func (t *FilesystemTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "operation",
			Type:        ParamString,
			Description: "Operation: read_file, write_file, edit_file, list_dir, glob, grep",
			Required:    true,
			Enum:        []string{"read_file", "write_file", "edit_file", "list_dir", "glob", "grep"},
		},
		{
			Name:        "path",
			Type:        ParamString,
			Description: "File or directory path",
			Required:    false,
		},
		{
			Name:        "content",
			Type:        ParamString,
			Description: "File content (write_file)",
			Required:    false,
		},
		{
			Name:        "search",
			Type:        ParamString,
			Description: "Search pattern (grep/edit_file)",
			Required:    false,
		},
		{
			Name:        "replace",
			Type:        ParamString,
			Description: "Replacement text (edit_file)",
			Required:    false,
		},
		{
			Name:        "pattern",
			Type:        ParamString,
			Description: "Glob pattern (glob)",
			Required:    false,
		},
		{
			Name:        "offset",
			Type:        ParamInteger,
			Description: "Line offset (read_file, 0-indexed)",
			Required:    false,
			Default:     0,
		},
		{
			Name:        "limit",
			Type:        ParamInteger,
			Description: "Line count (read_file)",
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

	switch operation {
	case "read_file":
		resolvedPath, err := t.resolveRequiredPath(workspace, args)
		if err != nil {
			return ToolResult{Error: err}
		}
		return t.readFile(t.containmentRoot(workspace, resolvedPath), resolvedPath, args)
	case "write_file":
		resolvedPath, err := t.resolveRequiredPath(workspace, args)
		if err != nil {
			return ToolResult{Error: err}
		}
		return t.writeFile(t.containmentRoot(workspace, resolvedPath), resolvedPath, args)
	case "edit_file":
		resolvedPath, err := t.resolveRequiredPath(workspace, args)
		if err != nil {
			return ToolResult{Error: err}
		}
		return t.editFile(t.containmentRoot(workspace, resolvedPath), resolvedPath, args)
	case "list_dir":
		resolvedPath, err := t.resolveRequiredPath(workspace, args)
		if err != nil {
			return ToolResult{Error: err}
		}
		return t.listDir(t.containmentRoot(workspace, resolvedPath), resolvedPath)
	case "glob":
		return t.glob(workspace, args)
	case "grep":
		return t.grep(workspace, args)
	default:
		return ToolResult{Error: fmt.Errorf("unknown operation: %s", operation)}
	}
}

func (t *FilesystemTool) resolveRequiredPath(workspace string, args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	return t.resolvePath(workspace, path)
}

// resolvePath resolves a path with workspace restriction.
func (t *FilesystemTool) resolvePath(workspace, path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}

	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Clean(filepath.Join(workspace, path))
	}

	// Resolve symlinks before the containment check so a lexically-inside path
	// that actually points outside the workspace (ws/link -> /etc) is rejected,
	// including a link whose target does not exist yet. The resolved path is
	// what we return and operate on, which narrows — but does not close — the
	// TOCTOU window: the final component can still be swapped for a symlink
	// between here and the open, which is why writes go through writeNoFollow.
	resolved, err := resolveSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve path %s: %w", path, err)
	}

	if t.restrict {
		// Compare against the symlink-resolved workspace too, so the workspace
		// itself lying behind a symlink (e.g. macOS /tmp -> /private/tmp) does
		// not spuriously fail the check.
		base := workspace
		if rb, berr := resolveSymlinks(filepath.Clean(workspace)); berr == nil {
			base = rb
		}
		if !isWithinBase(resolved, base) && !t.isAllowedResolved(resolved) {
			return "", fmt.Errorf("access denied: path %s is outside workspace %s", path, workspace)
		}
	}

	return resolved, nil
}

// containmentRoot picks the directory every subsequent open is walked from, for
// an already-resolved path. Containment is only meaningful relative to a root:
// the workspace normally, an explicitly allowed path when the operator named
// one, and the filesystem root when restriction is off (the walk still runs,
// refusing to traverse a symlink, which costs nothing because resolvePath has
// already resolved the path it returned).
//
// Roots are compared symlink-resolved on both sides so a workspace that itself
// lives behind a symlink (macOS /tmp -> /private/tmp) still matches.
func (t *FilesystemTool) containmentRoot(workspace, resolved string) string {
	if !t.restrict {
		return string(filepath.Separator)
	}

	base := filepath.Clean(workspace)
	if rb, err := resolveSymlinks(base); err == nil {
		base = rb
	}
	if isWithinBase(resolved, base) {
		return base
	}

	for _, allowed := range t.allowedPaths {
		a := filepath.Clean(allowed)
		if r, err := resolveSymlinks(a); err == nil {
			a = r
		}
		if isWithinBase(resolved, a) {
			return a
		}
	}

	// Not inside anything we would permit. resolvePath should already have
	// refused, but returning the workspace keeps the open contained rather than
	// falling back to "/" if a future caller reaches here without that check.
	return base
}

// safeReadFile reads path through the dirfd walk rooted at root, so no symlink
// at any component — not just the last — can redirect the read outside.
func safeReadFile(root, path string) ([]byte, error) {
	f, err := openInRoot(root, path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// safeWriteFile writes data to path through the same walk. The leaf is opened
// O_NOFOLLOW too, so an existing symlink there is refused rather than followed.
func safeWriteFile(root, path string, data []byte, perm os.FileMode) error {
	f, err := openInRoot(root, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, werr := f.Write(data); werr != nil {
		f.Close()
		return werr
	}
	return f.Close()
}

// readDirIn lists a directory through the same walk, sorted by name to match
// os.ReadDir's ordering.
func readDirIn(root, path string) ([]os.DirEntry, error) {
	f, err := openInRoot(root, path, os.O_RDONLY|openDirFlag, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// isAllowedPath checks if the path is in the allowed paths list.
func (t *FilesystemTool) isAllowedPath(path string) bool {
	for _, allowed := range t.allowedPaths {
		allowedClean := filepath.Clean(allowed)
		if isWithinBase(path, allowedClean) || path == allowedClean {
			return true
		}
	}
	return false
}

// isAllowedResolved reports whether a symlink-resolved path is within an
// allowed path, resolving each allowed base the same way so both sides are
// comparable (macOS /var -> /private/var, etc.).
func (t *FilesystemTool) isAllowedResolved(path string) bool {
	for _, allowed := range t.allowedPaths {
		allowedClean := filepath.Clean(allowed)
		if r, err := resolveSymlinks(allowedClean); err == nil {
			allowedClean = r
		}
		if isWithinBase(path, allowedClean) || path == allowedClean {
			return true
		}
	}
	return false
}

// readFile reads a file's contents.
func (t *FilesystemTool) readFile(root, path string, args map[string]any) ToolResult {
	offset := 0
	limit := 100

	if o, ok := args["offset"].(float64); ok {
		offset = int(o)
	}
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	data, err := safeReadFile(root, path)
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

	// Truncate output if it exceeds maxOutputChars
	if len(output) > t.maxOutputChars {
		truncated := output[:t.maxOutputChars]
		suffix := fmt.Sprintf("\n... (truncated, %d chars total)", len(output))
		output = truncated + suffix
	}

	return ToolResult{Output: output}
}

// writeFile writes content to a file.
func (t *FilesystemTool) writeFile(root, path string, args map[string]any) ToolResult {
	content, _ := args["content"].(string)

	// Ensure directory exists. mkdirAllIn, not os.MkdirAll: the latter resolves
	// intermediate components through symlinks, which is exactly the escape.
	if err := mkdirAllIn(root, filepath.Dir(path)); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to create directory: %w", err)}
	}

	if err := safeWriteFile(root, path, []byte(content), 0o644); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to write file: %w", err)}
	}

	return ToolResult{Output: fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path)}
}

// editFile performs search and replace on a file.
func (t *FilesystemTool) editFile(root, path string, args map[string]any) ToolResult {
	search, _ := args["search"].(string)
	replace, _ := args["replace"].(string)

	if search == "" {
		return ToolResult{Error: errors.New("search pattern is required")}
	}

	data, err := safeReadFile(root, path)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to read file: %w", err)}
	}

	content := string(data)
	modified := strings.Replace(content, search, replace, 1)

	if content == modified {
		return ToolResult{Error: errors.New("search pattern not found in file")}
	}

	if err := safeWriteFile(root, path, []byte(modified), 0o644); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to write file: %w", err)}
	}

	return ToolResult{Output: fmt.Sprintf("Successfully edited %s", path)}
}

// listDir lists directory contents.
func (t *FilesystemTool) listDir(root, path string) ToolResult {
	entries, err := readDirIn(root, path)
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

	if filepath.IsAbs(pattern) {
		pattern = filepath.Clean(pattern)
		if t.restrict && !isWithinBase(pattern, workspace) && !t.isAllowedPath(pattern) {
			return ToolResult{Error: fmt.Errorf("access denied: pattern %s is outside workspace %s", pattern, workspace)}
		}
	} else {
		// Resolve pattern relative to workspace
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
		if t.restrict && !isWithinBase(match, workspace) && !t.isAllowedPath(match) {
			continue
		}
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
	// Resolve even the default, so searchPath and the containment root are
	// expressed in the same (symlink-resolved) terms — otherwise every read below
	// would be rejected as outside the root on a system where the workspace
	// itself sits behind a symlink.
	target := path
	if target == "" {
		target = "."
	}
	searchPath, err := t.resolvePath(workspace, target)
	if err != nil {
		return ToolResult{Error: err}
	}
	root := t.containmentRoot(workspace, searchPath)

	var matches []string
	var filesSearched int

	// filepath.Walk uses Lstat, so it never descends *into* a symlinked
	// directory; the remaining exposure was the read of each matched file, which
	// went through os.ReadFile and would follow a symlink straight out of the
	// workspace. Each read is now contained by the same dirfd walk.
	werr := filepath.Walk(searchPath, func(p string, info fs.FileInfo, err error) error {
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

		data, err := safeReadFile(root, p)
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

	if werr != nil {
		return ToolResult{Error: fmt.Errorf("search failed: %w", werr)}
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
	Workspace      string
	Restrict       bool
	AllowedPaths   []string
	MaxOutputChars int
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

	maxOutputChars := cfg.MaxOutputChars
	if maxOutputChars == 0 {
		maxOutputChars = 8000
	}

	return NewFilesystemToolWithMaxOutput(workspace, cfg.Restrict, maxOutputChars, cfg.AllowedPaths...)
}
