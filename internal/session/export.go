package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/redact"
)

// Export writes a shareable copy of one session: a Markdown transcript and a
// JSON manifest describing it.
//
// The command exists to make a conversation attachable to a bug report, so the
// two properties that matter are that it is safe and that it is inert.
//
// Safe: every byte is passed through internal/redact before it is written, not
// after — credentials and the host home directory never reach the file at all,
// so there is no window in which an unredacted export exists on disk.
//
// Inert: the export reads the session and writes elsewhere. It performs no
// network request, and it does not go through Load, because Load quarantines
// corrupt input — an export that repaired the session it was asked to describe
// would change the very state a bug report is trying to capture. Corrupt lines
// are counted and reported in the manifest instead.
//
// The output is deterministic: nothing in either file comes from the wall clock
// at export time, only from timestamps already stored in the session. Two
// exports of an unchanged session are byte-identical, which is what lets a
// reporter show that a file was not edited between runs.

// ExportManifest describes an exported session. It is the machine-readable half
// of the export; the Markdown transcript is the human half.
type ExportManifest struct {
	// SchemaVersion allows a consumer to reject a manifest it cannot read.
	SchemaVersion int `json:"schema_version"`
	// SessionID is the session key, "channel:senderID".
	SessionID string `json:"session_id"`
	// Messages counts the messages that parsed and were exported.
	Messages int `json:"messages"`
	// SourceBytes is the size of the session file the digest was taken over.
	SourceBytes int64 `json:"source_bytes"`
	// SHA256 is the digest of the source session file, hex encoded. It lets a
	// reader confirm the transcript came from the file it claims to.
	SHA256 string `json:"sha256"`
	// CreatedAt and UpdatedAt come from the session's own message timestamps,
	// never from the clock at export time — the export must be reproducible.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Model and Workspace are redacted; either may be empty.
	Model     string `json:"model,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	// Topic is the stored conversation topic, redacted.
	Topic string `json:"topic,omitempty"`
	// Compacted reports that the session carries a compaction record, so a
	// reader knows the transcript is a summary plus a tail, not the whole
	// conversation.
	Compacted bool `json:"compacted"`
	// CorruptLines counts unparseable lines that were skipped. A non-zero value
	// means the transcript is incomplete, which a reader must be told rather
	// than left to infer from a suspiciously short conversation.
	CorruptLines int `json:"corrupt_lines"`
	// Roles tallies messages by role.
	Roles map[string]int `json:"roles"`
	// Tools tallies tool use by tool name.
	Tools []ExportToolUsage `json:"tools"`
}

// ExportToolUsage is the per-tool tally in the manifest. A slice rather than a
// map so the JSON has a stable order and the export stays byte-reproducible.
type ExportToolUsage struct {
	Name string `json:"name"`
	// Calls counts invocations; Results counts those that recorded a result.
	// A gap between the two is the signature of a turn that died mid-tool.
	Calls   int `json:"calls"`
	Results int `json:"results"`
}

// ExportResult reports what Export wrote.
type ExportResult struct {
	TranscriptPath string
	ManifestPath   string
	Manifest       ExportManifest
}

// exportSchemaVersion is bumped when the manifest's shape changes
// incompatibly.
const exportSchemaVersion = 1

// exportFileMode is deliberately owner-only, like the session file itself. An
// export is redacted, not sanitised: it still holds the whole conversation.
const exportFileMode = 0o600

// ExportPaths returns the two file paths an export of sessionID into outDir
// would write, without writing anything. The CLI uses it to report a conflict
// by name before doing any work.
func ExportPaths(outDir, sessionID string) (transcript, manifest string) {
	base := filepath.Join(outDir, sessionID+".export")
	return base + ".md", base + ".manifest.json"
}

// Export writes the transcript and manifest for sessionID into outDir.
//
// An existing file is never overwritten unless force is set: an export names
// itself after the session, so running it twice in a directory is the ordinary
// case, and silently replacing a copy someone had already attached to a report
// would destroy evidence.
func (m *Manager) Export(ctx context.Context, sessionID, outDir string, force bool) (ExportResult, error) {
	var zero ExportResult

	// Validate before touching the filesystem: the sender half of a session ID
	// is attacker-influenced, so a "/" or ".." must be refused before it is
	// joined into either an input or an output path.
	if err := ValidateSessionID(sessionID); err != nil {
		return zero, err
	}
	select {
	case <-ctx.Done():
		return zero, ErrContextCancelled
	default:
	}
	if strings.TrimSpace(outDir) == "" {
		outDir = "."
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	srcPath := m.sessionFilePath(sessionID)
	data, err := os.ReadFile(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, ErrSessionNotFound
		}
		return zero, fmt.Errorf("failed to read session %q: %w", sessionID, err)
	}

	messages, corrupt := parseExportMessages(data)
	digest := sha256.Sum256(data)

	manifest := buildExportManifest(sessionID, messages, corrupt, int64(len(data)), hex.EncodeToString(digest[:]))
	if meta, err := m.readExportMetadata(sessionID); err == nil {
		manifest.Model = redactExport(meta.ModelOverride)
		manifest.Topic = redactExport(meta.ConversationTopic)
		manifest.Workspace = redactExport(meta.ConversationContext["workspace"])
	}

	transcriptPath, manifestPath := ExportPaths(outDir, sessionID)
	if !force {
		for _, p := range []string{transcriptPath, manifestPath} {
			if _, err := os.Stat(p); err == nil {
				return zero, fmt.Errorf("%s already exists; re-run with --force to replace it", p)
			}
		}
	}

	transcript := renderExportMarkdown(manifest, messages)
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return zero, fmt.Errorf("failed to encode manifest: %w", err)
	}
	manifestJSON = append(manifestJSON, '\n')

	if err := writeFileAtomic(transcriptPath, []byte(transcript), exportFileMode); err != nil {
		return zero, fmt.Errorf("failed to write %s: %w", transcriptPath, err)
	}
	if err := writeFileAtomic(manifestPath, manifestJSON, exportFileMode); err != nil {
		// A transcript with no manifest is a half-export that reads as
		// complete. Roll it back so the failure is total and obvious.
		//
		// Only when this call created it: with --force over an existing pair,
		// removing the transcript would destroy the copy the operator already
		// had on the strength of a failure in the *other* file.
		if !force {
			_ = os.Remove(transcriptPath)
		}
		return zero, fmt.Errorf("failed to write %s: %w", manifestPath, err)
	}

	return ExportResult{
		TranscriptPath: transcriptPath,
		ManifestPath:   manifestPath,
		Manifest:       manifest,
	}, nil
}

// parseExportMessages reads the session's JSONL without side effects.
//
// Load is not reused: it writes a quarantine copy when it meets corrupt input,
// and an export must not modify the state it is describing.
func parseExportMessages(data []byte) ([]Message, int) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	messages := make([]Message, 0, len(lines))
	corrupt := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			corrupt++
			continue
		}
		messages = append(messages, msg)
	}
	return messages, corrupt
}

// exportMetadata mirrors the optional sidecar Load reads.
type exportMetadata struct {
	ConversationTopic   string            `json:"conversation_topic,omitempty"`
	ConversationContext map[string]string `json:"conversation_context,omitempty"`
	ModelOverride       string            `json:"model_override,omitempty"`
}

func (m *Manager) readExportMetadata(sessionID string) (exportMetadata, error) {
	var meta exportMetadata
	data, err := os.ReadFile(m.metadataFilePath(sessionID))
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

// buildExportManifest tallies the transcript. Every count comes from the
// messages themselves, so it is reproducible.
func buildExportManifest(sessionID string, messages []Message, corrupt int, size int64, digest string) ExportManifest {
	man := ExportManifest{
		SchemaVersion: exportSchemaVersion,
		SessionID:     sessionID,
		Messages:      len(messages),
		SourceBytes:   size,
		SHA256:        digest,
		CorruptLines:  corrupt,
		Roles:         map[string]int{},
	}
	if len(messages) > 0 {
		man.CreatedAt = messages[0].Timestamp
		man.UpdatedAt = messages[len(messages)-1].Timestamp
		man.Compacted = messages[0].IsCompaction()
	}

	calls := map[string]int{}
	results := map[string]int{}
	for _, msg := range messages {
		man.Roles[string(msg.Role)]++
		for _, tc := range msg.ToolCalls {
			calls[tc.Name]++
			if tc.Result != "" {
				results[tc.Name]++
			}
		}
	}
	names := make([]string, 0, len(calls))
	for name := range calls {
		names = append(names, name)
	}
	sort.Strings(names)
	man.Tools = make([]ExportToolUsage, 0, len(names))
	for _, name := range names {
		man.Tools = append(man.Tools, ExportToolUsage{
			Name:    name,
			Calls:   calls[name],
			Results: results[name],
		})
	}
	return man
}

// redactExport strips credentials and the host home directory from one string.
//
// Both passes are needed and the order matters little, but neither may be
// skipped: redact.String finds credential-shaped values, redact.HomePath
// rewrites "/Users/someone/..." to "~/...", which is a username the reporter
// did not intend to publish.
func redactExport(s string) string {
	return redact.HomePath(redact.String(s))
}

// renderExportMarkdown builds the human-readable transcript.
//
// Every dynamic value goes through redactExport on the way in. Redacting the
// finished document instead would be equivalent today, but it invites a later
// change that writes a field directly — doing it per field means a new field
// that forgets it is visibly missing a call.
func renderExportMarkdown(man ExportManifest, messages []Message) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Session %s\n\n", redactExport(man.SessionID))
	if man.Topic != "" {
		fmt.Fprintf(&b, "- Topic: %s\n", man.Topic)
	}
	if man.Model != "" {
		fmt.Fprintf(&b, "- Model: %s\n", man.Model)
	}
	if man.Workspace != "" {
		fmt.Fprintf(&b, "- Workspace: %s\n", man.Workspace)
	}
	fmt.Fprintf(&b, "- Messages: %d\n", man.Messages)
	if !man.CreatedAt.IsZero() {
		fmt.Fprintf(&b, "- First message: %s\n", man.CreatedAt.UTC().Format(time.RFC3339))
		fmt.Fprintf(&b, "- Last message: %s\n", man.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if man.Compacted {
		fmt.Fprintln(&b, "- Compacted: the first entry is a summary of earlier turns, not a message")
	}
	if man.CorruptLines > 0 {
		fmt.Fprintf(&b, "- **Incomplete: %d unreadable line(s) in the source file were skipped.**\n", man.CorruptLines)
	}
	fmt.Fprintln(&b, "\nCredentials and home-directory paths are redacted. Everything else is verbatim.")

	for i, msg := range messages {
		fmt.Fprintf(&b, "\n---\n\n## %d. %s", i+1, msg.Role)
		if !msg.Timestamp.IsZero() {
			fmt.Fprintf(&b, " — %s", msg.Timestamp.UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(&b)

		if content := strings.TrimRight(redactExport(msg.Content), "\n"); content != "" {
			fmt.Fprintf(&b, "\n%s\n", content)
		}
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(&b, "\n### tool: %s\n", redactExport(tc.Name))
			// Fenced so a tool argument containing Markdown cannot restructure
			// the document it is quoted in.
			fmt.Fprintf(&b, "\nArguments:\n```json\n%s\n```\n",
				redactExport(strings.TrimSpace(string(tc.Arguments))))
			if tc.Result != "" {
				fmt.Fprintf(&b, "\nResult:\n```\n%s\n```\n",
					redactExport(strings.TrimRight(tc.Result, "\n")))
			}
		}
	}
	return b.String()
}
