package session

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// FormatInfoTable renders a session inventory.
//
// Rendering lives here rather than in cmd/joshbot so it can be tested directly:
// the CLI package is thin on purpose and sits far below the coverage floor.
func FormatInfoTable(w io.Writer, infos []Info, now time.Time) {
	if len(infos) == 0 {
		fmt.Fprintln(w, "No sessions yet. One is created the first time you talk to joshbot.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tMESSAGES\tSIZE\tUPDATED\tNOTES")
	for _, info := range infos {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n",
			info.ID,
			info.Messages,
			humanBytes(info.Bytes),
			humanAge(now, info.UpdatedAt),
			strings.Join(notes(info), ", "),
		)
	}
	_ = tw.Flush()

	fmt.Fprintf(w, "\n%d session(s).\n", len(infos))
}

// notes surfaces the states an operator needs to see without reading the files.
func notes(info Info) []string {
	var out []string
	if info.Unreadable {
		// Say what is actually known. Reporting "quarantine file present" for a
		// session that merely failed to open told the operator something untrue.
		out = append(out, "unreadable (could not be scanned)")
	} else if info.Corrupt {
		if info.CorruptLines > 0 {
			out = append(out, fmt.Sprintf("corrupt (%d unreadable line(s))", info.CorruptLines))
		} else {
			out = append(out, "corrupt (quarantine file present)")
		}
	}
	if info.Compacted {
		out = append(out, "compacted")
	}
	if info.ArchiveBytes > 0 {
		out = append(out, "archive "+humanBytes(info.ArchiveBytes))
	}
	if len(out) == 0 {
		out = append(out, "-")
	}
	return out
}

// FormatMessages renders a session transcript, most recent last.
//
// last <= 0 means "all"; a value larger than the number of messages is not an
// error, it simply shows everything.
func FormatMessages(w io.Writer, msgs []Message, last int) {
	if len(msgs) == 0 {
		fmt.Fprintln(w, "(this session has no messages)")
		return
	}
	if last > 0 && last < len(msgs) {
		msgs = msgs[len(msgs)-last:]
	}

	for _, msg := range msgs {
		marker := ""
		if msg.Compaction {
			marker = " [compaction record]"
		}
		fmt.Fprintf(w, "\n--- %s%s %s\n", msg.Role, marker, msg.Timestamp.Format(time.RFC3339))
		if content := strings.TrimRight(msg.Content, "\n"); content != "" {
			fmt.Fprintln(w, content)
		}
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(w, "  -> tool %s(%s)\n", tc.Name, string(tc.Arguments))
		}
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// humanAge renders an age rather than a timestamp: "3 days ago" answers the
// question a prune is about, where an ISO timestamp needs mental arithmetic.
func humanAge(now, then time.Time) string {
	if then.IsZero() {
		return "unknown"
	}
	d := now.Sub(then)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
