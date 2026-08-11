package main

import (
	"errors"
	"io"
	"os"

	"github.com/bigknoxy/joshbot/internal/output"
	"github.com/bigknoxy/joshbot/internal/redact"
	"github.com/urfave/cli/v2"
)

// outputFormat resolves the global --output flag. An unknown value is a
// validation error, not a general failure: exitValidation is what this repo
// already uses for "the invocation itself is wrong", and it is what lets a
// script tell a typo apart from a command that ran and reported a problem.
func outputFormat(c *cli.Context) (output.Format, error) {
	f, err := output.ParseFormat(c.String("output"))
	if err != nil {
		return "", newExitError(exitValidation, "pass a valid --output value", err)
	}
	return f, nil
}

// reportWriter is where a reporting command's payload goes. Always through the
// redactor: these documents exist to be pasted into an issue or piped into a
// script, and a credential must not survive either trip.
func reportWriter() io.Writer { return redact.Writer(os.Stdout) }

// jsonWriter is where a JSON document goes: plain stdout, deliberately not
// through the redactor. The redactor rewrites a finished byte stream and
// recognises "<name><sep><value>" pairs, which an encoded document is made of —
// it turned `"has_credential": true` into `"has_credential": [REDACTED]`, i.e.
// not JSON at all. JSON documents are made safe as they are built instead; see
// the internal/output package comment.
func jsonWriter() io.Writer { return os.Stdout }

// withJSONErrors makes a command's failure machine-readable when --output json
// is in force. The error document goes to stdout, not stderr, because a caller
// that asked for JSON on stdout should not need a second reader to discover
// why the command failed; the process still exits with the code the error
// carries, so `set -e` behaves exactly as before.
//
// When --output is text (or unparseable — the parse error is itself the thing
// being reported) this is a no-op and urfave/cli prints the error as usual.
func withJSONErrors(fn cli.ActionFunc) cli.ActionFunc {
	return func(c *cli.Context) error {
		err := fn(c)
		if err == nil {
			return nil
		}
		format, parseErr := output.ParseFormat(c.String("output"))
		if parseErr != nil || format != output.JSON {
			return err
		}
		// A command that already wrote its own document signals that by
		// returning cli.Exit("", code) — it wants the exit status, not a second
		// report. Emitting an ErrorDoc here too would put two documents on
		// stdout, and the second one with an empty message at that.
		var coder cli.ExitCoder
		if errors.As(err, &coder) && coder.Error() == "" {
			return err
		}
		code := codeForError(err)
		_ = output.WriteError(jsonWriter(), code, err)
		// cli.Exit with an empty message: the document above is the report,
		// and urfave/cli would otherwise print the same error again as text
		// on stderr, defeating the point of a machine-readable mode.
		return cli.Exit("", code)
	}
}
