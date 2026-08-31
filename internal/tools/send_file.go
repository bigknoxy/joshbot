package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// SendFileTool delivers a workspace file to a chat channel as a native
// attachment.
//
// It is a separate tool from `message`, not a mode of it. Two reasons. The
// message tool requires non-empty content, and a file sent with no caption is
// an ordinary request that must not have to invent one. And the two have
// entirely different preconditions — a path that resolves inside the
// workspace, a size under the transport's limit, sniffed content — which as a
// branch inside one Execute would be checked only on some calls and read as
// optional.
//
// Containment is not advisory here. The bytes leave the process, so the path
// goes through the same openat walk the filesystem tool reads through: an
// intermediate component that has become a symlink out of the workspace fails
// the walk rather than being followed, and nothing is published.
type SendFileTool struct {
	sender MessageSender
	fs     *FilesystemTool
	limits bus.AttachmentLimits
	// approval gates a send behind a human decision, off by default. See
	// approval.go and ShellTool.checkApproval — this reuses the same
	// Approver plumbing (rides the request context via WithApprover) rather
	// than growing a second approval mechanism.
	approval ApprovalMode
}

// SetApproval turns the human-approval gate on for outbound sends. The
// approver comes from the request context (WithApprover); a request with
// none is denied outright rather than left blocking (see ApproverFromContext).
func (t *SendFileTool) SetApproval(mode ApprovalMode) {
	if mode == "" {
		mode = ApprovalOff
	}
	t.approval = mode
}

// NewSendFileTool creates a SendFileTool contained the same way the filesystem
// tool is, so the agent can only send files it could legitimately read.
func NewSendFileTool(sender MessageSender, cfg FilesystemToolConfig) *SendFileTool {
	return &SendFileTool{
		sender: sender,
		fs: NewFilesystemToolFromConfig(FilesystemToolConfig{
			Workspace:    cfg.Workspace,
			Restrict:     cfg.Restrict,
			AllowedPaths: cfg.AllowedPaths,
		}),
		limits: bus.DefaultAttachmentLimits(),
	}
}

// Limits returns the size limits this tool enforces. It is an accessor rather
// than a set of constants read at each use site so a transport that can carry
// more (a self-hosted Telegram Bot API server, issue #280) has exactly one
// place to raise them.
func (t *SendFileTool) Limits() bus.AttachmentLimits { return t.limits.WithDefaults() }

// SetLimits overrides the size limits. A zero field keeps the default.
func (t *SendFileTool) SetLimits(l bus.AttachmentLimits) { t.limits = l.WithDefaults() }

// Name returns the name of the tool.
func (t *SendFileTool) Name() string { return "send_file" }

// Description returns a description of the tool.
func (t *SendFileTool) Description() string {
	return `Send a file from the workspace to a chat as a native attachment. Images are sent inline as photos, everything else as a downloadable document. Use this instead of pasting file contents into your reply.`
}

// Parameters returns the parameters for the tool.
func (t *SendFileTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "path",
			Type:        ParamString,
			Description: "Path to the file to send. Must be inside the workspace.",
			Required:    true,
		},
		{
			Name:        "caption",
			Type:        ParamString,
			Description: "Optional text to show with the file",
			Required:    false,
		},
	}
}

// Execute reads, validates and publishes the file.
func (t *SendFileTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if t.sender == nil {
		return ToolResult{Error: errors.New("message sender not configured")}
	}

	execCtx := context.Background()
	workspace := t.fs.workspace
	if ctx != nil {
		if c, ok := ctx.(context.Context); ok && c != nil {
			execCtx = c
			if w, ok := c.Value(ContextKeyWorkspace).(string); ok && w != "" {
				workspace = w
			}
		}
	}

	path, _ := args["path"].(string)
	caption, _ := args["caption"].(string)

	// The recipient comes from the inbound turn, never from the model: the tool
	// has no address argument at all. A default here would be a silent
	// wrong-recipient send, which is strictly worse than a failure — so an
	// unattached turn is refused.
	channel := ChannelFromContext(execCtx)
	if channel == "" {
		return ToolResult{Error: errors.New("no channel on this turn: send_file can only reply to the conversation it was called from")}
	}

	att, err := t.buildAttachment(workspace, path)
	if err != nil {
		return ToolResult{Error: err}
	}

	// The gate runs after containment resolves the file, not before: a path
	// refused by containment must never reach a human as a prompt, the same
	// ordering rule the shell tool's deny list follows ahead of its approval
	// gate.
	if err := t.checkApproval(execCtx, att, channel); err != nil {
		return ToolResult{Error: err}
	}

	if err := t.sender.SendFile(execCtx, channel, att, caption); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to send file: %w", err)}
	}
	return ToolResult{Output: fmt.Sprintf("Sent %s (%s, %s) to %s",
		att.Filename, att.Kind, humanBytes(att.Size), channel)}
}

// checkApproval runs the human-approval gate, returning nil when the send
// may proceed. Mirrors ShellTool.checkApproval: the prompt names the file,
// its size and the recipient channel, since those are the three things the
// operator is actually deciding about.
func (t *SendFileTool) checkApproval(ctx context.Context, att Attachment, channel string) error {
	if t.approval == ApprovalOff || t.approval == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	req := ApprovalRequest{
		Tool:    "send_file",
		Command: fmt.Sprintf("send %s (%s) to %s", att.SourcePath, humanBytes(att.Size), channel),
	}
	decision, err := ApproverFromContext(ctx).Approve(ctx, req)
	if decision == Approve && err == nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDenied, strings.TrimPrefix(err.Error(), ErrDenied.Error()+": "))
	}
	return fmt.Errorf("%w: run it yourself, or ask the user to approve it", ErrDenied)
}

// buildAttachment resolves, contains, measures and sniffs the file. It is
// separate from Execute so the whole validation path is testable without a
// sender, and so a failure at any step returns before anything is published.
func (t *SendFileTool) buildAttachment(workspace, path string) (Attachment, error) {
	resolved, err := t.fs.resolvePath(workspace, path)
	if err != nil {
		return Attachment{}, err
	}
	root := t.fs.containmentRoot(workspace, resolved)

	f, err := openInRoot(root, resolved, os.O_RDONLY, 0)
	if err != nil {
		return Attachment{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Attachment{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return Attachment{}, fmt.Errorf("%s is a directory, not a file", path)
	}
	size := info.Size()
	if size == 0 {
		return Attachment{}, fmt.Errorf("%s is empty, nothing to send", path)
	}

	limits := t.Limits()
	if size > limits.DocumentMaxBytes {
		return Attachment{}, fmt.Errorf("%s is %s, over the %s limit for an outbound file",
			path, humanBytes(size), humanBytes(limits.DocumentMaxBytes))
	}

	// Sniff from a bounded head read. http.DetectContentType never looks past
	// 512 bytes, so this is the whole evidence either way, and it keeps a
	// 50 MiB document from being read into memory just to classify it.
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return Attachment{}, fmt.Errorf("read %s: %w", path, err)
	}
	head = head[:n]

	att := Attachment{
		Filename:   filepath.Base(resolved),
		MIME:       sniffAttachmentMIME(head),
		Size:       size,
		SourcePath: relativeLabel(workspace, resolved),
	}

	// Content decides the routing, never the extension: a .png holding prose
	// must not be offered to Telegram as a photo (it answers 400), and a .dat
	// holding a JPEG should still render inline. An image over the photo limit
	// is not an error — it is a document, which is how the recipient can still
	// receive it.
	if providers.IsSupportedImageMIME(att.MIME) && size <= limits.PhotoMaxBytes {
		att.Kind = bus.AttachmentPhoto
	} else {
		att.Kind = bus.AttachmentDocument
	}

	// The bytes always ride the message, read from the same handle the
	// contained walk opened. This is the only outbound read there is: nothing
	// downstream re-opens the path, so a leaf swapped for a symlink after this
	// point changes nothing about what is sent. The size ceiling above is what
	// bounds the memory this costs.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Attachment{}, fmt.Errorf("read %s: %w", path, err)
	}
	data, err := io.ReadAll(io.LimitReader(f, size))
	if err != nil {
		return Attachment{}, fmt.Errorf("read %s: %w", path, err)
	}
	att.Data = data

	return att, nil
}

// relativeLabel renders a resolved path as a workspace-relative label. It is
// never opened — it exists so a channel that cannot carry attachments can name
// the file — so an absolute path here would only leak the operator's home
// directory into chat. filepath.Rel can fail (different volumes, an
// allowed_paths file outside the workspace); the basename is the honest
// fallback, and the absolute path is never published either way.
func relativeLabel(workspace, resolved string) string {
	rel, err := filepath.Rel(workspace, resolved)
	if err == nil && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return filepath.Base(resolved)
}

// SetSender sets the message sender.
func (t *SendFileTool) SetSender(sender MessageSender) { t.sender = sender }

// sniffAttachmentMIME detects a type from content alone, dropping the charset
// parameter DetectContentType appends to text types.
func sniffAttachmentMIME(head []byte) string {
	mime := http.DetectContentType(head)
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	return mime
}

// humanBytes formats a size the way the limits are written, so an error can be
// compared to the constant it names without arithmetic.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
