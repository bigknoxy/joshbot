package tools

import (
	"path"
	"regexp"
	"strings"
)

// This file implements the screening layer that decides whether a shell
// command is too dangerous to run.
//
// It is defence in depth, NOT a security boundary. No deny list can be sound
// against an adversarial model or a prompt-injected instruction; real
// isolation needs an OS-level sandbox (container, seccomp, Landlock). What
// this layer does is close the bypasses that plain substring matching left
// open, while refusing as few legitimate commands as possible -- a screener
// that blocks everyday work gets switched off, and then it protects nothing.
//
// Closed evasion classes, each covered by a test in shell_deny_test.go:
//
//	rm -rf  /              padded whitespace
//	rm -fr / , rm -r -f /  reordered and split flags
//	r"m" -rf /             quote splicing
//	sudo -u root rm -rf /  wrapper commands, including their value-taking flags
//	sh -c "rm -rf /"       interpreter -c arguments (screened recursively)
//	(rm -rf /) , { ...; }  subshells and brace groups
//	if true; then ...; fi  shell keywords
//	echo "$(rm -rf /)"     substitution inside double quotes
//	curl x |<newline>sh    pipe state across an empty segment
//	rm -rf // , /home/../  paths that only resolve to root
//	$(rm -rf /) , `...`    command substitution
//	rm -rf$IFS/            IFS separators
//
// Known and accepted gaps: indirection through multiplexers (`busybox` is
// unwrapped but `xargs`, `find -exec` beyond the cases below, and interpreter
// programs read from a file are not), and anything the model can reach by
// writing a script and running it. These are why the OS boundary matters.

var (
	whitespaceRun = regexp.MustCompile(`\s+`)

	// forkBomb matches a function that pipes into itself and backgrounds,
	// e.g. :(){:|:&};: and any renamed variant of it.
	forkBomb = regexp.MustCompile(`[a-z_:][a-z0-9_]*\(\)\{[^}]*\|[^}]*&[^}]*\}`)

	// deviceWrite matches a redirect onto a raw block device.
	deviceWrite = regexp.MustCompile(`>\s*/dev/(sd|hd|nvme|vd|xvd)`)

	// assignment matches a leading VAR=value token.
	assignment = regexp.MustCompile(`^[a-z_][a-z0-9_]*=`)

	// numeric matches a bare number, e.g. the duration argument to timeout.
	numeric = regexp.MustCompile(`^[0-9]+[smhd]?$`)

	// varExpansion matches an unexpanded parameter reference. Screening
	// cannot see through one, so eval of such a string is unscreenable.
	varExpansion = regexp.MustCompile(`\$\{?[a-z_][a-z0-9_]*\}?`)
)

// Screening runs synchronously before the command's timeout context exists,
// so it must be bounded in both depth and input size. Each level of $( )
// nesting copies the remaining body, making deeply nested input quadratic:
// 20k levels cost seconds and gigabytes. Both caps fail CLOSED — an input we
// cannot screen is refused rather than permitted.
const (
	maxScreenDepth = 4
	maxScreenInput = 16384
)

// commandWrappers take another command as their argument, so the wrapped
// command is what actually needs screening.
var commandWrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "nohup": true, "time": true,
	"command": true, "builtin": true, "exec": true, "nice": true,
	"ionice": true, "stdbuf": true, "setsid": true, "timeout": true,
	"unbuffer": true, "script": true, "busybox": true,
}

// wrapperValueFlags lists the flags of each wrapper that consume the token
// after them. Without this, "sudo -u root rm -rf /" screens as command
// "root" and every rule below is bypassed.
var wrapperValueFlags = map[string]map[string]bool{
	"sudo":    {"-u": true, "-g": true, "-U": true, "-C": true, "-p": true, "-r": true, "-t": true, "-h": true},
	"doas":    {"-u": true, "-C": true},
	"env":     {"-u": true, "-C": true, "-S": true},
	"nice":    {"-n": true},
	"ionice":  {"-c": true, "-n": true, "-p": true},
	"stdbuf":  {"-i": true, "-o": true, "-e": true},
	"timeout": {"-s": true, "-k": true},
	"time":    {"-o": true, "-f": true},
	"exec":    {"-a": true},
	"script":  {"-c": true},
}

// shellKeywords introduce a compound command; the interesting command word
// follows them.
var shellKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"while": true, "until": true, "do": true, "done": true, "for": true,
	"case": true, "esac": true, "select": true, "function": true, "!": true,
}

// shellInterpreters execute their -c argument as shell code, and executing
// whatever arrives on stdin is their entire purpose.
var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"ash": true, "csh": true, "tcsh": true, "fish": true,
}

// scriptInterpreters run code in another language. Piping data into one is
// ordinary work (`cat x.json | python3 -m json.tool`), so they are only a
// concern when fed by a fetcher or a decoder, or when an inline program
// shells out.
var scriptInterpreters = map[string]bool{
	"python": true, "python2": true, "python3": true, "perl": true,
	"ruby": true, "node": true, "php": true, "awk": true, "gawk": true,
	"mawk": true,
}

// untrustedSources produce bytes from the network or from an encoding, so
// piping their output into any interpreter is remote code execution.
var untrustedSources = map[string]bool{
	"curl": true, "wget": true, "fetch": true, "nc": true, "ncat": true,
	"netcat": true, "base64": true, "xxd": true, "openssl": true, "uudecode": true,
}

// execSinks are the ways an inline program in another language reaches back
// out to a shell.
var execSinks = []string{
	"os.system", "subprocess", "popen", "child_process", "system(",
	"exec(", "spawn(", "shell_exec", "passthru(", "kernel.exec", "%x{",
}

// dangerousPaths are roots where a recursive delete or ownership change is
// never a legitimate request.
var dangerousPaths = map[string]bool{
	"": true, "~": true, "$home": true, "${home}": true,
	"/home": true, "/root": true, "/etc": true, "/usr": true, "/var": true,
	"/bin": true, "/sbin": true, "/lib": true, "/lib64": true, "/boot": true,
	"/dev": true, "/proc": true, "/sys": true, "/opt": true, "/srv": true,
}

// shellSegment is one command within a command line, already stripped of
// quoting and normalised for matching.
type shellSegment struct {
	text  string // lowercased, unquoted, whitespace-collapsed
	piped bool   // this segment reads the stdout of the previous one
	async bool   // this segment was backgrounded with a bare &
	from  string // command word of the segment feeding this one, if piped
}

// splitSegments breaks a command line into individual commands at separators
// that are not inside quotes. Quoting is removed as it goes, so `r"m"` and
// `'rm'` both normalise to `rm`, while `echo "a; reboot"` stays a single
// segment whose command word is echo.
//
// Command substitutions are recognised inside double quotes as well as
// outside, because the shell expands them in both; only single quotes
// suppress them. Their bodies are screened as segments of their own.
func splitSegments(cmd string) []shellSegment {
	var (
		segs     []shellSegment
		subs     []string
		cur      strings.Builder
		sub      strings.Builder
		inSingle bool
		inDouble bool
		depth    int
		piped    bool
	)

	flush := func(async bool) {
		text := normalizeSegment(cur.String())
		cur.Reset()
		// An empty segment must not consume a pending pipe: in
		// "curl x |\n sh" the newline produces nothing, and resetting here
		// would let the interpreter through.
		if text == "" {
			return
		}
		segs = append(segs, shellSegment{text: text, piped: piped, async: async})
		piped = false
	}

	r := []rune(cmd)
	for i := 0; i < len(r); i++ {
		c := r[i]

		// Inside a $( ) substitution: capture the body for a recursive pass
		// and track nesting so $(a $(b)) closes correctly.
		if depth > 0 {
			switch c {
			case '(':
				depth++
			case ')':
				if depth--; depth == 0 {
					subs = append(subs, sub.String())
					sub.Reset()
					continue
				}
			}
			sub.WriteRune(c)
			continue
		}

		if c == '\\' && !inSingle && i+1 < len(r) {
			i++
			cur.WriteRune(r[i]) // the escaped character is a literal
			continue
		}
		// ANSI-C quoting: $'rm' is the word rm, so the $ must not become
		// part of the command name.
		if c == '$' && !inSingle && !inDouble && i+1 < len(r) && r[i+1] == '\'' {
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}

		// Substitution and ${...} are checked before the quote guard: the
		// shell expands them inside double quotes too.
		if !inSingle && c == '$' && i+1 < len(r) && r[i+1] == '(' {
			depth, i = 1, i+1
			continue
		}
		if !inSingle && c == '`' {
			j := i + 1
			for j < len(r) && r[j] != '`' {
				j++
			}
			subs = append(subs, string(r[i+1:min(j, len(r))]))
			i = j
			continue
		}
		if !inSingle && c == '$' && i+1 < len(r) && r[i+1] == '{' {
			// A parameter expansion, not a brace group. Keep it inline so
			// normalizeSegment can expand ${IFS}.
			j := i
			for j < len(r) && r[j] != '}' {
				cur.WriteRune(r[j])
				j++
			}
			if j < len(r) {
				cur.WriteRune('}')
			}
			i = j
			continue
		}

		if inSingle || inDouble {
			cur.WriteRune(c)
			continue
		}

		switch c {
		case ';', '\n', '(', ')', '{', '}':
			// Subshells and brace groups are command boundaries. Without
			// this, "(rm -rf /)" screens as command "(rm".
			flush(false)
		case '|':
			if i+1 < len(r) && r[i+1] == '|' { // || is control flow, not a pipe
				flush(false)
				i++
				continue
			}
			flush(false)
			piped = true
		case '&':
			// Keep redirect forms (&>, &>>, 2>&1, >&2) as literal text.
			if strings.HasSuffix(cur.String(), ">") || (i+1 < len(r) && r[i+1] == '>') {
				cur.WriteRune(c)
				continue
			}
			if i+1 < len(r) && r[i+1] == '&' { // && is control flow
				flush(false)
				i++
				continue
			}
			flush(true) // a bare & backgrounds the preceding command
		default:
			cur.WriteRune(c)
		}
	}
	flush(false)

	// An unterminated $( leaves its body uncaptured. Screening what we did
	// read is the fail-closed choice; discarding it would let
	// "$(rm -rf /" through with zero segments.
	if sub.Len() > 0 {
		subs = append(subs, sub.String())
	}

	// Record what feeds each piped segment, so a fetcher piped into an
	// interpreter can be told apart from a local file piped into one.
	for i := 1; i < len(segs); i++ {
		if segs[i].piped {
			if t := effectiveTokens(segs[i-1].text); len(t) > 0 {
				segs[i].from = t[0]
			}
		}
	}

	for _, s := range subs {
		segs = append(segs, splitSegments(s)...)
	}
	return segs
}

// normalizeSegment lowercases a segment and collapses whitespace, expanding
// the $IFS separator trick so "rm -rf$IFS/" screens as "rm -rf /".
func normalizeSegment(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("${ifs}", " ", "$ifs", " ").Replace(s)
	return strings.TrimSpace(whitespaceRun.ReplaceAllString(s, " "))
}

// effectiveTokens returns a segment's tokens with leading VAR=value
// assignments, shell keywords and wrapper commands removed, so that
// "sudo -u root nohup /bin/rm -rf /" screens as "rm -rf /".
func effectiveTokens(seg string) []string {
	tokens := strings.Fields(seg)
	wrapper := ""
	skipped := false

	for len(tokens) > 0 {
		head := tokens[0]
		base := path.Base(head)

		switch {
		case assignment.MatchString(head):
		case shellKeywords[base]:
			skipped = true
		case commandWrappers[base]:
			wrapper, skipped = base, true
		case skipped && strings.HasPrefix(head, "-"):
			// Drop the flag, and its value when this wrapper takes one.
			if wrapperValueFlags[wrapper][head] && len(tokens) > 1 {
				tokens = tokens[1:]
			}
		case skipped && numeric.MatchString(head):
		default:
			out := make([]string, len(tokens))
			copy(out, tokens)
			out[0] = base
			return out
		}
		tokens = tokens[1:]
	}
	return nil
}

// hasShortFlag reports whether args contains a short flag, including when it
// is bundled with others (-rf, -fr and -r -f all carry 'r' and 'f').
func hasShortFlag(args []string, flag byte) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") || strings.HasPrefix(a, "--") {
			continue
		}
		if strings.IndexByte(a[1:], flag) >= 0 {
			return true
		}
	}
	return false
}

// hasLongFlag reports whether args contains --name.
func hasLongFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name || strings.HasPrefix(a, "--"+name+"=") {
			return true
		}
	}
	return false
}

// isReadOnly reports whether a disk or mount command was invoked in one of
// its inspection modes, which are safe and commonly asked for.
func isReadOnly(args []string) bool {
	return hasShortFlag(args, 'l') || hasLongFlag(args, "list") ||
		hasShortFlag(args, 'n') || hasLongFlag(args, "dry-run")
}

// operands returns the non-flag arguments, honouring the -- terminator.
func operands(args []string) []string {
	var out []string
	end := false
	for _, a := range args {
		if !end && a == "--" {
			end = true
			continue
		}
		if !end && strings.HasPrefix(a, "-") && a != "-" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// isDangerousPath reports whether target resolves to a system root that
// should never be the subject of a recursive operation. Cleaning first is
// what stops "//", "/.", "/home/../" and "/usr/../" from slipping past.
func isDangerousPath(target string) bool {
	if target == "" {
		return true
	}
	t := path.Clean(target)
	t = strings.TrimSuffix(t, "/*")
	t = strings.TrimSuffix(t, "/")
	return dangerousPaths[t]
}

// inlineProgram returns the argument passed to an interpreter's -c/-e flag,
// which is code that will actually run.
func inlineProgram(args []string) (string, bool) {
	for i, a := range args {
		if a == "-c" || a == "-e" || a == "--command" {
			if i+1 < len(args) {
				return strings.Join(args[i+1:], " "), true
			}
			return "", true
		}
		if strings.HasPrefix(a, "-c") && len(a) > 2 {
			return a[2:] + " " + strings.Join(args[i+1:], " "), true
		}
	}
	return "", false
}

// screenSegment returns a non-empty reason if a single command is dangerous.
func screenSegment(seg shellSegment, depth int) string {
	tokens := effectiveTokens(seg.text)
	if len(tokens) == 0 {
		return ""
	}
	cmd, args := tokens[0], tokens[1:]

	if seg.piped && shellInterpreters[cmd] {
		return "pipe into shell (" + cmd + ")"
	}
	// Piping a local file into python is ordinary work; piping a download or
	// a decoded blob into it is remote code execution.
	if seg.piped && scriptInterpreters[cmd] && untrustedSources[seg.from] {
		return "pipe from " + seg.from + " into interpreter (" + cmd + ")"
	}
	if hasLongFlag(args, "no-preserve-root") {
		return "--no-preserve-root"
	}
	if strings.HasPrefix(cmd, "mkfs") {
		return "filesystem creation (" + cmd + ")"
	}

	// An interpreter's -c argument is what actually executes, so screen it
	// rather than stopping at the interpreter's own name.
	if shellInterpreters[cmd] && depth < maxScreenDepth {
		if prog, ok := inlineProgram(args); ok && prog != "" {
			if reason := screenAt(prog, nil, depth+1); reason != "" {
				return reason
			}
		}
	}
	if scriptInterpreters[cmd] {
		prog, ok := inlineProgram(args)
		if !ok && (cmd == "awk" || cmd == "gawk" || cmd == "mawk") {
			prog = strings.Join(operands(args), " ")
		}
		for _, sink := range execSinks {
			if strings.Contains(prog, sink) {
				return "inline " + cmd + " program shells out (" + sink + ")"
			}
		}
	}

	switch cmd {
	case "eval":
		// The $(...) bodies of `eval "$(direnv hook bash)"` have already been
		// screened as their own segments, so that idiom is fine. What cannot
		// be screened is eval of a variable whose contents are unknown here.
		rest := strings.Join(args, " ")
		if varExpansion.MatchString(rest) {
			return "eval of an unexpanded variable (contents cannot be screened)"
		}
		if rest != "" && depth < maxScreenDepth {
			if reason := screenAt(rest, nil, depth+1); reason != "" {
				return reason
			}
		}

	case "rm":
		if hasShortFlag(args, 'r') || hasLongFlag(args, "recursive") {
			for _, target := range operands(args) {
				if isDangerousPath(target) {
					return "recursive delete of " + target
				}
			}
		}

	case "find":
		// find / -delete and find / -exec rm are deletes wearing a disguise.
		if hasLongFlag(args, "delete") || strings.Contains(seg.text, "-delete") ||
			strings.Contains(seg.text, "-exec rm") {
			for _, target := range operands(args) {
				if isDangerousPath(target) {
					return "recursive delete of " + target + " via find"
				}
			}
		}

	case "fdisk", "cfdisk", "sfdisk", "parted", "wipefs", "blkdiscard":
		if isReadOnly(args) {
			return "" // -l and -n are inspection modes
		}
		return "disk manipulation (" + cmd + ")"

	case "shred":
		// Shredding a file is the intended use; shredding a device is not.
		for _, o := range operands(args) {
			if strings.HasPrefix(o, "/dev/") {
				return "shred of a device (" + o + ")"
			}
		}

	case "mount", "umount":
		// Bare `mount` and `mount -l` just list what is mounted.
		if len(operands(args)) == 0 {
			return ""
		}
		return "filesystem mount (" + cmd + ")"

	case "dd":
		for _, a := range args {
			if strings.HasPrefix(a, "if=") || strings.HasPrefix(a, "of=") {
				return "dd raw device access"
			}
		}

	case "shutdown", "reboot", "halt", "poweroff":
		return "system power state (" + cmd + ")"

	case "init", "telinit":
		if o := operands(args); len(o) > 0 && (o[0] == "0" || o[0] == "6") {
			return "init " + o[0]
		}

	case "systemctl":
		for _, a := range args {
			switch a {
			case "poweroff", "reboot", "halt", "shutdown", "kexec", "emergency":
				return "systemctl " + a
			}
		}

	case "kill", "pkill", "killall":
		// Killing a named process is routine; killing everything is not.
		if hasShortFlag(args, '9') {
			if len(operands(args)) == 0 {
				return cmd + " -9 with no target"
			}
			for _, a := range args {
				if a == "-1" || a == "1" {
					return cmd + " -9 of all processes"
				}
			}
		}

	case "chmod":
		if hasShortFlag(args, 'r') || hasLongFlag(args, "recursive") {
			for _, o := range operands(args) {
				switch o {
				case "777", "0777", "a+rwx":
					return "recursive world-writable chmod"
				}
				if isDangerousPath(o) {
					return "recursive chmod of " + o
				}
			}
		}

	case "chown", "chgrp":
		if hasShortFlag(args, 'r') || hasLongFlag(args, "recursive") {
			for _, o := range operands(args) {
				if isDangerousPath(o) {
					return "recursive ownership change of " + o
				}
			}
		}
	}

	return ""
}

// screenCommandLine applies the rules that need the whole command line rather
// than a single segment.
func screenCommandLine(cmd string, segs []shellSegment) string {
	// The fork bomb survives segmentation, so match it on the raw text with
	// quoting and whitespace removed.
	despaced := whitespaceRun.ReplaceAllString(strings.ToLower(cmd), "")
	despaced = strings.NewReplacer(`"`, "", `'`, "", `\`, "").Replace(despaced)
	if forkBomb.MatchString(despaced) {
		return "fork bomb"
	}

	for _, seg := range segs {
		if deviceWrite.MatchString(seg.text) {
			return "redirect onto a raw block device"
		}
		if seg.async {
			return "background execution"
		}
	}
	return ""
}

// screenAt screens a command line at a given recursion depth.
func screenAt(cmd string, custom []string, depth int) string {
	if depth > maxScreenDepth {
		return "command nested too deeply to screen"
	}
	if len(cmd) > maxScreenInput {
		return "command too long to screen safely"
	}
	segs := splitSegments(cmd)

	if reason := screenCommandLine(cmd, segs); reason != "" {
		return reason
	}
	for _, seg := range segs {
		if reason := screenSegment(seg, depth); reason != "" {
			return reason
		}
	}

	for _, pattern := range custom {
		p := normalizeSegment(pattern)
		if p == "" {
			continue
		}
		for _, seg := range segs {
			if strings.Contains(seg.text, p) {
				return pattern
			}
		}
	}
	return ""
}

// screen returns a non-empty reason if cmd should not be executed.
// Custom patterns from configuration are matched as substrings against the
// normalised text, and apply in addition to the built-in rules above.
func screen(cmd string, custom []string) string {
	return screenAt(cmd, custom, 0)
}
