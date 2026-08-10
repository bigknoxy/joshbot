// Package output renders the read-only CLI commands in either the human text
// form joshbot has always printed or a machine-readable JSON document, selected
// by the global --output flag (issue #131).
//
// The split exists so `cmd/joshbot` keeps only flag wiring: every struct and
// every renderer lives here, where it is cheap to test. Two properties are the
// whole point of the package and both are pinned by tests:
//
//   - The text rendering is byte-identical to what the command printed before
//     the flag existed. A consumer parsing stdout with awk must not notice.
//   - The JSON document carries data only. It is deterministic (no map
//     iteration, no wall clock), it goes to stdout alone, and no configured
//     credential can appear in it.
//
// Redaction works differently in the two forms, and the difference is load
// bearing. The text form is written through internal/redact, which rewrites the
// finished byte stream. That cannot be done to JSON: the redactor recognises
// "<secret-name><sep><value>" anywhere it appears, and the encoded document is
// full of such pairs by construction — it turned `"has_credential": true` into
// `"has_credential": [REDACTED]`, which is not valid JSON at all, so a caller
// that asked for a machine-readable document got something no parser accepts.
//
// So JSON is made safe *before* encoding instead. The document types carry no
// credential fields — the preflight report answers "is there a credential" with
// a bool and "where did it come from" with an enum, never with the credential —
// and the two fields that do carry arbitrary text, ConfigError and the error
// message, are passed through redact.String by NewPreflight and WriteError as
// they are built. Tests assert per command that a configured api_key does not
// appear in the document.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/bigknoxy/joshbot/internal/redact"
)

// SchemaVersion is stamped into every document. Bump it when a field changes
// meaning or disappears; adding a field is backwards compatible and does not
// need a bump.
const SchemaVersion = 1

// Format selects a rendering. The zero value is Text, so a caller that forgets
// to parse the flag gets the historical behaviour rather than JSON.
type Format string

const (
	Text Format = "text"
	JSON Format = "json"
)

// Formats lists the valid --output values in the order they are shown to the
// user. Keep it in sync with the constants above; ParseFormat and the usage
// string both read it.
var Formats = []string{string(Text), string(JSON)}

// ParseFormat maps a --output value onto a Format. An unknown value is a usage
// error naming the valid ones — the caller turns it into exit code 3
// (exitValidation), which is
// how a script tells "you spelled the flag wrong" apart from "the command ran
// and failed".
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case Text, JSON:
		return Format(s), nil
	case "":
		return Text, nil
	default:
		return "", fmt.Errorf("invalid --output %q: valid values are %s", s, strings.Join(Formats, ", "))
	}
}

// WriteJSON emits one document followed by a newline. Indented, because these
// documents are read by humans debugging a script at least as often as by the
// script itself, and `jq` does not care either way.
func WriteJSON(w io.Writer, doc any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// HTML escaping would turn a workspace path containing & into & for
	// no benefit: this output is never embedded in a page.
	enc.SetEscapeHTML(false)
	return enc.Encode(doc)
}

// ErrorDoc is the single document emitted in JSON mode when a command fails.
// It goes to stdout, not stderr: a consumer that selected --output json asked
// for the result on stdout, and splitting success and failure across two
// streams means every caller needs two readers to find out what happened.
type ErrorDoc struct {
	SchemaVersion int       `json:"schema_version"`
	Error         ErrorBody `json:"error"`
}

// ErrorBody carries the machine-readable code and the human message.
type ErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// WriteError emits the failure document. code is the process exit code the
// caller is about to use, so the document and the shell agree.
//
// The message is the one field of any document built from arbitrary text — an
// error can quote a config value, a URL or a provider response — so it is
// redacted here, before encoding, rather than by wrapping the writer.
func WriteError(w io.Writer, code int, err error) error {
	return WriteJSON(w, ErrorDoc{
		SchemaVersion: SchemaVersion,
		Error:         ErrorBody{Code: code, Message: redact.String(err.Error())},
	})
}
