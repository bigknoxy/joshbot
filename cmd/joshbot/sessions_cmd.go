package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/cron"
	"github.com/bigknoxy/joshbot/internal/redact"
	"github.com/bigknoxy/joshbot/internal/session"
)

// Operator-facing commands for inspecting and clearing conversation state.
//
// Sessions are keyed "channel:senderID" and loaded on every inbound message, so
// there is exactly one per user per channel and nothing to "resume". What was
// missing was the ability to see what exists, read one back, and clear or
// remove one — in particular, a supported way to recover a user whose session
// was damaged, which previously meant hand-deleting a file under
// ~/.joshbot/sessions.
//
// All logic lives in internal/session. This file is argument parsing and
// output plumbing.

// usageError is returned for bad arguments so the CLI exits 2 rather than 1.
func usageError(format string, args ...any) error {
	return cli.Exit(fmt.Sprintf(format, args...), 2)
}

// sessionsCommand builds the `joshbot sessions` command group.
func sessionsCommand() *cli.Command {
	return &cli.Command{
		Name:  "sessions",
		Usage: "Inspect and manage stored conversations",
		Description: "Sessions are keyed channel:senderID and stored as JSONL under\n" +
			"~/.joshbot/sessions. There is one per user per channel; it is loaded\n" +
			"automatically on every message, so there is nothing to resume.\n\n" +
			"Output is redacted: credentials and your home directory are stripped\n" +
			"before display. The files on disk are left verbatim.",
		Subcommands: []*cli.Command{
			{
				Name:   "list",
				Usage:  "List stored sessions with size, message count and age",
				Action: runSessionsList,
			},
			{
				Name:      "show",
				Usage:     "Print a session's messages (redacted)",
				ArgsUsage: "<session id>",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "last",
						Usage: "Show only the last N messages (default: all)",
					},
				},
				Action: runSessionsShow,
			},
			{
				Name:      "search",
				Usage:     "Search every session transcript for a phrase (redacted output)",
				ArgsUsage: "<query>",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "limit",
						Usage: "Max matches to show (default 10)",
					},
				},
				Action: runSessionsSearch,
			},
			{
				Name:      "prune",
				Usage:     "Delete a session, or every session older than a duration",
				ArgsUsage: "[session id]",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "older-than",
						Usage: "Delete every session untouched for this long (e.g. 30d, 12h)",
					},
					&cli.BoolFlag{
						Name:  "force",
						Usage: "Skip the confirmation prompt",
					},
				},
				Action: runSessionsPrune,
			},
			{
				Name:      "export",
				Usage:     "Write a redacted Markdown transcript and JSON manifest",
				ArgsUsage: "<session id>",
				Description: "Writes <id>.export.md and <id>.export.manifest.json. Credentials and\n" +
					"home-directory paths are stripped before anything is written, the\n" +
					"session on disk is not modified, and two exports of an unchanged\n" +
					"session are byte-identical.",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "out",
						Usage: "Directory to write the export into (default: current directory)",
					},
					&cli.BoolFlag{
						Name:  "force",
						Usage: "Replace an existing export",
					},
				},
				Action: runSessionsExport,
			},
			{
				Name:      "new",
				Usage:     "Archive a session and start it empty",
				ArgsUsage: "<session id>",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "force",
						Usage: "Skip the confirmation prompt",
					},
				},
				Action: runSessionsNew,
			},
		},
	}
}

// sessionManagerForCLI builds a session manager without starting the app.
func sessionManagerForCLI(c *cli.Context) (*session.Manager, error) {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return nil, err
	}
	mgr, err := session.NewManager(cfg.SessionsDir())
	if err != nil {
		return nil, fmt.Errorf("failed to open sessions: %w", err)
	}
	return mgr, nil
}

// sessionsOut is the redacted writer every subcommand prints through.
//
// A session transcript is the single most credential-dense thing joshbot
// stores, and `sessions show` exists to be read and pasted.
var sessionsOut = func() io.Writer { return redact.Writer(os.Stdout) }

func runSessionsList(c *cli.Context) error {
	mgr, err := sessionManagerForCLI(c)
	if err != nil {
		return err
	}

	infos, err := mgr.ListInfo(c.Context)
	if err != nil {
		return err
	}

	session.FormatInfoTable(sessionsOut(), infos, time.Now())
	return nil
}

func runSessionsShow(c *cli.Context) error {
	parsed, err := parseSessionArgs(c.Args().Slice(), true, false, false)
	if err != nil {
		return usageError("%v", err)
	}
	id := strings.TrimSpace(parsed.id)
	if id == "" {
		return usageError("sessions show needs a session id (see: joshbot sessions list)")
	}

	last := c.Int("last")
	lastSet := c.IsSet("last")
	if parsed.lastSet {
		last, lastSet = parsed.last, true
	}
	if lastSet && last <= 0 {
		return usageError("--last must be a positive number, got %d", last)
	}

	mgr, err := sessionManagerForCLI(c)
	if err != nil {
		return err
	}

	sess, err := mgr.Load(c.Context, id)
	if err != nil {
		return sessionError(id, err)
	}

	out := sessionsOut()
	fmt.Fprintf(out, "Session: %s (%d message(s))\n", sess.ID, len(sess.Messages))
	if sess.ConversationTopic != "" {
		fmt.Fprintf(out, "Topic:   %s\n", sess.ConversationTopic)
	}
	session.FormatMessages(out, sess.Messages, last)
	return nil
}

func runSessionsPrune(c *cli.Context) error {
	parsed, err := parseSessionArgs(c.Args().Slice(), false, true, false)
	if err != nil {
		return usageError("%v", err)
	}
	id := strings.TrimSpace(parsed.id)
	force := c.Bool("force") || parsed.force
	olderThan := strings.TrimSpace(c.String("older-than"))
	if parsed.olderThanSet {
		olderThan = strings.TrimSpace(parsed.olderThan)
	}

	if id == "" && olderThan == "" {
		return usageError("sessions prune needs a session id or --older-than")
	}
	if id != "" && olderThan != "" {
		return usageError("give either a session id or --older-than, not both")
	}

	mgr, err := sessionManagerForCLI(c)
	if err != nil {
		return err
	}
	out := sessionsOut()

	if olderThan != "" {
		// cron.ParseDuration is reused because it already accepts the "d"
		// suffix operators expect here; time.ParseDuration does not.
		d, err := cron.ParseDuration(olderThan)
		if err != nil {
			return usageError("invalid --older-than %q: %v", olderThan, err)
		}
		if d <= 0 {
			return usageError("--older-than must be positive, got %q", olderThan)
		}

		cutoff := time.Now().Add(-d)
		if !force {
			infos, err := mgr.ListInfo(c.Context)
			if err != nil {
				return err
			}
			var doomed []string
			for _, info := range infos {
				if info.UpdatedAt.Before(cutoff) {
					doomed = append(doomed, info.ID)
				}
			}
			if len(doomed) == 0 {
				fmt.Fprintf(out, "No sessions older than %s.\n", olderThan)
				return nil
			}
			if !confirmDestructive(out, fmt.Sprintf("Delete %d session(s) older than %s: %s",
				len(doomed), olderThan, strings.Join(doomed, ", "))) {
				fmt.Fprintln(out, "Aborted. Nothing was deleted.")
				return nil
			}
		}

		removed, err := mgr.PruneOlderThan(c.Context, cutoff)
		if err != nil {
			return err
		}
		if len(removed) == 0 {
			fmt.Fprintf(out, "No sessions older than %s.\n", olderThan)
			return nil
		}
		fmt.Fprintf(out, "Deleted %d session(s): %s\n", len(removed), strings.Join(removed, ", "))
		return nil
	}

	// Confirm against a real session so a typo does not read as "nothing to do".
	if _, err := mgr.Stat(c.Context, id); err != nil {
		return sessionError(id, err)
	}
	if !force && !confirmDestructive(out, fmt.Sprintf("Delete session %q", id)) {
		// Non-zero: a script must not read a refusal as a completed delete.
		fmt.Fprintln(out, "Aborted. Nothing was deleted.")
		return cli.Exit("", 1)
	}
	if err := mgr.Delete(c.Context, id); err != nil {
		return sessionError(id, err)
	}
	fmt.Fprintf(out, "Deleted session %q.\n", id)
	return nil
}

func runSessionsNew(c *cli.Context) error {
	parsed, err := parseSessionArgs(c.Args().Slice(), false, false, false)
	if err != nil {
		return usageError("%v", err)
	}
	id := strings.TrimSpace(parsed.id)
	if id == "" {
		return usageError("sessions new needs a session id (see: joshbot sessions list)")
	}
	force := c.Bool("force") || parsed.force

	mgr, err := sessionManagerForCLI(c)
	if err != nil {
		return err
	}
	out := sessionsOut()

	if !force && !confirmDestructive(out,
		fmt.Sprintf("Archive session %q and start it empty", id)) {
		fmt.Fprintln(out, "Aborted. Nothing was changed.")
		return cli.Exit("", 1)
	}

	archived, err := mgr.Reset(c.Context, id)
	if err != nil {
		return sessionError(id, err)
	}
	fmt.Fprintf(out, "Session %q is now empty. Previous history archived as %s\n", id, archived)
	return nil
}

// runSessionsExport writes a shareable copy of one session.
//
// It prints where the files went and, when the source had unreadable lines,
// says so — an export that quietly dropped part of the conversation would be
// worse than no export, because it reads as complete.
func runSessionsExport(c *cli.Context) error {
	parsed, err := parseSessionArgs(c.Args().Slice(), false, false, true)
	if err != nil {
		return usageError("%v", err)
	}
	id := strings.TrimSpace(parsed.id)
	if id == "" {
		return usageError("sessions export needs a session id (see: joshbot sessions list)")
	}
	force := c.Bool("force") || parsed.force
	outDir := strings.TrimSpace(c.String("out"))
	if parsed.outSet {
		outDir = strings.TrimSpace(parsed.out)
	}
	if outDir == "" {
		outDir = "."
	}

	mgr, err := sessionManagerForCLI(c)
	if err != nil {
		return err
	}

	res, err := mgr.Export(c.Context, id, outDir, force)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, session.ErrInvalidSessionID) {
			return sessionError(id, err)
		}
		return cli.Exit(redact.String(err.Error()), 1)
	}

	out := sessionsOut()
	fmt.Fprintf(out, "Exported session %q (%d message(s)):\n", id, res.Manifest.Messages)
	fmt.Fprintf(out, "  %s\n", res.TranscriptPath)
	fmt.Fprintf(out, "  %s\n", res.ManifestPath)
	if res.Manifest.CorruptLines > 0 {
		fmt.Fprintf(out, "Warning: %d unreadable line(s) were skipped; the transcript is incomplete.\n",
			res.Manifest.CorruptLines)
	}
	return nil
}

// sessionError turns a store error into an actionable CLI failure.
func sessionError(id string, err error) error {
	switch {
	case errors.Is(err, session.ErrSessionNotFound):
		return cli.Exit(fmt.Sprintf("No session %q. Run 'joshbot sessions list' to see what exists.", id), 1)
	case errors.Is(err, session.ErrInvalidSessionID):
		return cli.Exit(fmt.Sprintf("Invalid session id %q: %v", id, err), 2)
	default:
		return err
	}
}

// confirmDestructive prompts unless stdin is not a terminal.
//
// A non-interactive run must never block waiting for input that will not come;
// it declines instead, and --force is the documented way through. That matches
// the rest of the CLI, where every destructive command is scriptable via
// --force.
var confirmDestructive = func(out io.Writer, action string) bool {
	if !isTTY(os.Stdin) {
		fmt.Fprintf(out, "%s: refusing without a terminal. Re-run with --force.\n", action)
		return false
	}
	fmt.Fprintf(out, "%s? [y/N] ", action)
	var answer string
	_, _ = fmt.Fscanln(os.Stdin, &answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// sessionArgs is the parsed form of a subcommand's arguments.
type sessionArgs struct {
	id           string
	last         int
	lastSet      bool
	force        bool
	olderThan    string
	olderThanSet bool
	out          string
	outSet       bool
}

// parseSessionArgs reads the positional id and any flags that trail it.
//
// urfave/cli v2 stops flag parsing at the first non-flag argument, so
// `sessions prune my-id --force` leaves --force unparsed. That is not a
// cosmetic problem: the destructive commands would silently decline, and
// `show my-id --last 2` would silently print the *entire* transcript instead of
// the two messages that were asked for. Flags before the id are handled by
// urfave as usual; this handles the ones after it, so both orders work.
//
// Unknown flags are an error rather than being ignored, so a typo cannot look
// like it worked.
func parseSessionArgs(args []string, allowLast, allowOlderThan, allowOut bool) (sessionArgs, error) {
	var out sessionArgs

	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "--force":
			out.force = true

		case arg == "--last" || strings.HasPrefix(arg, "--last="):
			if !allowLast {
				return out, fmt.Errorf("unknown flag %q", "--last")
			}
			var raw string
			if v, ok := strings.CutPrefix(arg, "--last="); ok {
				raw = v
			} else {
				if i+1 >= len(args) {
					return out, fmt.Errorf("--last needs a value")
				}
				i++
				raw = args[i]
			}
			n, err := strconv.Atoi(raw)
			if err != nil {
				return out, fmt.Errorf("--last needs a number, got %q", raw)
			}
			out.last, out.lastSet = n, true

		case arg == "--older-than" || strings.HasPrefix(arg, "--older-than="):
			if !allowOlderThan {
				return out, fmt.Errorf("unknown flag %q", "--older-than")
			}
			var raw string
			if v, ok := strings.CutPrefix(arg, "--older-than="); ok {
				raw = v
			} else {
				if i+1 >= len(args) {
					return out, fmt.Errorf("--older-than needs a value")
				}
				i++
				raw = args[i]
			}
			out.olderThan, out.olderThanSet = raw, true

		case arg == "--out" || strings.HasPrefix(arg, "--out="):
			if !allowOut {
				return out, fmt.Errorf("unknown flag %q", "--out")
			}
			var raw string
			if v, ok := strings.CutPrefix(arg, "--out="); ok {
				raw = v
			} else {
				if i+1 >= len(args) {
					return out, fmt.Errorf("--out needs a value")
				}
				i++
				raw = args[i]
			}
			out.out, out.outSet = raw, true

		case strings.HasPrefix(arg, "-"):
			return out, fmt.Errorf("unknown flag %q", arg)

		case out.id == "":
			out.id = arg

		default:
			return out, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	return out, nil
}

// runSessionsSearch greps every transcript for a phrase. The query is every
// positional argument joined, so quoting is optional; a trailing --limit is
// parsed by hand because urfave/cli stops flag parsing at the first
// positional (the `prune <id> --force` trap, same treatment).
func runSessionsSearch(c *cli.Context) error {
	limit := c.Int("limit")
	var words []string
	args := c.Args().Slice()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--limit" && i+1 < len(args):
			if n, err := strconv.Atoi(args[i+1]); err == nil {
				limit = n
			}
			i++
		case strings.HasPrefix(arg, "--limit="):
			if n, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit=")); err == nil {
				limit = n
			}
		default:
			words = append(words, arg)
		}
	}
	query := strings.TrimSpace(strings.Join(words, " "))
	if query == "" {
		return exitErrorf(exitValidation, "usage: joshbot sessions search <query> [--limit N]")
	}
	if limit <= 0 {
		limit = 10
	}

	mgr, err := sessionManagerForCLI(c)
	if err != nil {
		return err
	}
	matches, err := mgr.Search(c.Context, query, limit)
	if err != nil {
		return exitErrorf(exitGeneral, "search failed: %w", err)
	}
	out := sessionsOut()
	if len(matches) == 0 {
		fmt.Fprintf(out, "No matches for %q.\n", query)
		return nil
	}
	for _, m := range matches {
		fmt.Fprintf(out, "%s  %s  %s\n    %s\n",
			m.Timestamp.Format("2006-01-02 15:04"), m.SessionID, m.Role, m.Snippet)
	}
	fmt.Fprintf(out, "\n%d match(es).\n", len(matches))
	return nil
}
