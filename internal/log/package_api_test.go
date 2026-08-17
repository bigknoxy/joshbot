package log

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newGlobalLogFile points the *global* logger — the one every package-level
// function in this file reaches through Get() — at a file in a temp dir, and
// returns a reader for it.
//
// os.Stdout is swapped for /dev/null only while NewLogger runs, because
// NewLogger captures the value of os.Stdout at construction; the point is to
// keep the test's own log records out of the test binary's output, not to
// change what is being tested. The multiwriter, and with it redact.Writer, is
// exactly the production one.
//
// No t.Parallel anywhere in this file: `global`, `globalOnce` and os.Stdout are
// all process-global.
func newGlobalLogFile(t *testing.T) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "global.log")

	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	old := os.Stdout
	os.Stdout = devnull

	globalOnce = sync.Once{}
	global = nil
	initErr := Init(Config{Level: DebugLevel, File: path, Prefix: "test"})

	os.Stdout = old
	if initErr != nil {
		_ = devnull.Close()
		t.Fatalf("Init: %v", initErr)
	}

	t.Cleanup(func() {
		_ = Close()
		_ = devnull.Close()
		globalOnce = sync.Once{}
		global = nil
	})

	return func() string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read global log: %v", err)
		}
		return string(data)
	}
}

// The redaction boundary is on the writer, so it must hold for every entry
// point — not only the *Logger methods the existing tests exercise. The
// package-level functions are what the rest of joshbot actually calls, and a
// tool result carrying a credential reaches them by the most ordinary route
// there is.
func TestPackageLevelFunctionsAreRedactedEndToEnd(t *testing.T) {
	read := newGlobalLogFile(t)

	const key = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv"
	Debug("debug msg", "out", "cat config gave "+key)
	Debugf("debugf %s", "OPENAI_API_KEY=abc123def456ghi")
	Info("info msg", "out", key)
	Infof("infof %s", key)
	Warn("warn msg", "out", key)
	Warnf("warnf %s", key)
	Error("error msg", "out", key)
	Errorf("errorf %s", key)
	Log(InfoLevel, "log msg", "out", key)
	Logf(WarnLevel, "logf %s", key)

	ctx := ContextWithTraceID(context.Background(), "trace-abc")
	DebugContext(ctx, "debugctx msg", "out", key)
	DebugContextf(ctx, "debugctxf %s", key)
	InfoContext(ctx, "infoctx msg", "out", key)
	InfoContextf(ctx, "infoctxf %s", key)
	WarnContext(ctx, "warnctx msg", "out", key)
	WarnContextf(ctx, "warnctxf %s", key)
	ErrorContext(ctx, "errorctx msg", "out", key)
	ErrorContextf(ctx, "errorctxf %s", key)
	LogContext(ctx, InfoLevel, "logctx msg", "out", key)
	LogContextf(ctx, ErrorLevel, "logctxf %s", key)

	// Child loggers must not escape the boundary either — they share the
	// writer, and With/SubPackage/WithContext are how most call sites get one.
	With("k", "v").Info("with msg", "out", key)
	SubPackage("sub").Info("subpackage msg", "out", key)
	WithContext(ctx).Info("withcontext msg", "out", key)

	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	content := read()

	if strings.Contains(content, key) || strings.Contains(content, "abc123def456ghi") {
		t.Fatalf("a credential reached the log through a package-level function:\n%s", content)
	}
	// Every one of the calls above has to have produced a record: a function
	// that silently drops its message would also "pass" a leak check.
	for _, want := range []string{
		"debug msg", "debugf", "info msg", "infof", "warn msg", "warnf",
		"error msg", "errorf", "log msg", "logf",
		"debugctx msg", "debugctxf", "infoctx msg", "infoctxf",
		"warnctx msg", "warnctxf", "errorctx msg", "errorctxf",
		"logctx msg", "logctxf",
		"with msg", "subpackage msg", "withcontext msg",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("record %q missing from the log:\n%s", want, content)
		}
	}
	if strings.Count(content, "[REDACTED]") < 20 {
		t.Errorf("expected every record to be redacted, got:\n%s", content)
	}
}

// WithContext and ContextLogger must attach the trace ID, and must mint one
// when the context carries none — a log line with no trace ID cannot be
// correlated with the turn that produced it, which is the only reason the
// context variants exist.
func TestContextLoggersAttachATraceID(t *testing.T) {
	read := newGlobalLogFile(t)

	WithContext(ContextWithTraceID(context.Background(), "trace-known")).Info("carried")
	WithContext(context.Background()).Info("minted")
	ContextLogger(context.Background(), "k", "v").Info("clmint")
	ContextLogger(ContextWithTraceID(context.Background(), "trace-cl"), "k", "v").Info("clcarried")

	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	content := read()

	lines := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		for _, msg := range []string{"carried", "minted", "clmint", "clcarried"} {
			if strings.Contains(line, " "+msg+" ") || strings.HasSuffix(line, " "+msg) {
				lines[msg] = line
			}
		}
	}
	for _, msg := range []string{"carried", "minted", "clmint", "clcarried"} {
		if lines[msg] == "" {
			t.Fatalf("record %q missing:\n%s", msg, content)
		}
	}

	// WithContext always stamps a trace ID: the caller's when the context has
	// one, a fresh one when it does not.
	if !strings.Contains(lines["carried"], "trace_id=trace-known") {
		t.Errorf("WithContext dropped the context's trace ID: %q", lines["carried"])
	}
	if !strings.Contains(lines["minted"], "trace_id=trace-") || strings.Contains(lines["minted"], "trace_id=\"\"") {
		t.Errorf("WithContext minted no trace ID for a bare context: %q", lines["minted"])
	}

	// ContextLogger always stamps a trace ID: the caller's when the context has
	// one, a freshly generated one when it does not. It differs from WithContext
	// only in that it also merges in the caller's keyValues — but both always
	// put the trace ID on the returned logger.
	if !strings.Contains(lines["clmint"], "trace_id=trace-") {
		t.Errorf("ContextLogger minted no trace ID for a bare context: %q", lines["clmint"])
	}
	if !strings.Contains(lines["clcarried"], "trace_id=trace-cl") {
		t.Errorf("ContextLogger dropped the context's trace ID: %q", lines["clcarried"])
	}
	if !strings.Contains(lines["clmint"], "k=v") || !strings.Contains(lines["clcarried"], "k=v") {
		t.Errorf("ContextLogger dropped the caller's key-values:\n%s", content)
	}
}

// SetTraceIDFunc is how a caller substitutes its own request ID scheme; if it
// is ignored, log lines cannot be joined to anything upstream.
func TestSetTraceIDFuncIsUsedWhenTheContextHasNone(t *testing.T) {
	read := newGlobalLogFile(t)

	old := traceIDFunc
	t.Cleanup(func() { traceIDFunc = old })
	SetTraceIDFunc(func() string { return "custom-trace-id" })

	WithContext(context.Background()).Info("wc")
	ContextLogger(context.Background()).Info("cl")
	InfoContext(context.Background(), "ic")
	if got := ContextWithTraceID(context.Background(), ""); TraceIDFromContext(got) != "custom-trace-id" {
		t.Errorf("ContextWithTraceID ignored the custom generator: %q", TraceIDFromContext(got))
	}

	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := strings.Count(read(), "custom-trace-id"); got != 3 {
		t.Errorf("custom trace ID appears %d times, want 3:\n%s", got, read())
	}
}

// Close is called on shutdown and must release the file handles it opened; it
// must also be safe when nothing was ever initialised, which is the state a
// command that fails during flag parsing shuts down from.
func TestCloseOnUninitialisedGlobalIsANoop(t *testing.T) {
	saved := global
	t.Cleanup(func() {
		global = saved
		globalOnce = sync.Once{}
	})

	global = nil
	if err := Close(); err != nil {
		t.Errorf("Close with no global logger: %v", err)
	}

	// A nil handler in the slice must not panic either — a logger built with
	// no File has an empty slice, and a partially built one can hold a nil.
	global = &Logger{handlers: []io.WriteCloser{nil}}
	if err := Close(); err != nil {
		t.Errorf("Close with a nil handler: %v", err)
	}
}

// The log file holds tool results and message content, so a failure to open or
// narrow it has to be reported, never swallowed into a logger that silently
// writes to stdout only.
func TestNewLoggerReportsAnUnusableLogPath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewLogger(Config{Level: InfoLevel, File: filepath.Join(blocker, "joshbot.log")}); err == nil {
		t.Fatal("NewLogger accepted a log path whose directory cannot be created")
	} else if !strings.Contains(err.Error(), "log directory") {
		t.Errorf("error does not name the cause: %v", err)
	}
}

// Init reports the construction failure rather than leaving a nil global that
// panics on first use.
func TestInitPropagatesTheConstructionError(t *testing.T) {
	saved := global
	t.Cleanup(func() {
		global = saved
		globalOnce = sync.Once{}
	})

	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	globalOnce = sync.Once{}
	global = nil
	if err := Init(Config{Level: InfoLevel, File: filepath.Join(blocker, "a.log")}); err == nil {
		t.Fatal("Init accepted an unusable log file")
	}
}

// ---- Fatal variants ----

// fatalCases are run in a re-exec of this test binary, because each one calls
// os.Exit. Every variant is listed: a Fatal that failed to exit would leave a
// caller running past a condition it declared unrecoverable, and a Fatal that
// leaked a credential would put one in the last thing a crashing process
// printed — which is precisely the text that gets pasted into a bug report.
const fatalKey = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv"

var fatalCases = map[string]func(){
	"Fatal":              func() { Fatal("fatal msg", "out", fatalKey) },
	"FatalNoArgs":        func() { Fatal() },
	"Fatalf":             func() { Fatalf("fatalf %s", fatalKey) },
	"FatalContext":       func() { FatalContext(context.Background(), "fatal msg", "out", fatalKey) },
	"FatalContextNoArgs": func() { FatalContext(context.Background()) },
	"FatalContextf":      func() { FatalContextf(context.Background(), "fatalf %s", fatalKey) },
	"MethodFatal":        func() { Get().Fatal("fatal msg", "out", fatalKey) },
	"MethodFatalNoArgs":  func() { Get().Fatal() },
	"MethodFatalf":       func() { Get().Fatalf("fatalf %s", fatalKey) },
}

func TestFatalVariantsExitAndStayRedacted(t *testing.T) {
	if name := os.Getenv("JOSHBOT_LOG_FATAL_CASE"); name != "" {
		fn, ok := fatalCases[name]
		if !ok {
			t.Fatalf("unknown fatal case %q", name)
		}
		fn()
		// Reached only if Fatal did not exit.
		t.Fatalf("%s returned instead of exiting", name)
	}

	for name := range fatalCases {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestFatalVariantsExitAndStayRedacted")
			cmd.Env = append(os.Environ(), "JOSHBOT_LOG_FATAL_CASE="+name)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s did not exit non-zero; output:\n%s", name, out)
			}
			text := string(out)
			if strings.Contains(text, fatalKey) {
				t.Errorf("%s leaked a credential into the dying process's output:\n%s", name, text)
			}
			if strings.Contains(name, "NoArgs") {
				return
			}
			if !strings.Contains(text, "fatal msg") && !strings.Contains(text, "fatalf") {
				t.Errorf("%s exited without logging its message:\n%s", name, text)
			}
			if !strings.Contains(text, "[REDACTED]") {
				t.Errorf("%s did not redact its message:\n%s", name, text)
			}
		})
	}
}

// ---- Instance methods ----

// The *Logger methods take ...any and pull the message out of args[0]. The
// empty call is the branch that has no message to pull, and it must still
// produce a record rather than panic on args[0].
func TestLoggerMethodsWithNoArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.log")
	l, err := NewLogger(Config{Level: DebugLevel, File: path, Prefix: "t"})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	l.Debug()
	l.Info()
	l.Warn()
	l.Error()
	l.Log(InfoLevel)
	l.With("k", "v").Info()

	for _, h := range l.handlers {
		_ = h.Close()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; got != 6 {
		t.Errorf("expected 6 records from the no-argument methods, got %d:\n%s", got, data)
	}
}

// The package-level functions have the same empty-args branch, reached by
// callers that log only structured fields.
func TestPackageFunctionsWithNoArguments(t *testing.T) {
	read := newGlobalLogFile(t)

	Debug()
	Info()
	Warn()
	Error()
	Log(InfoLevel)
	ctx := context.Background()
	DebugContext(ctx)
	InfoContext(ctx)
	WarnContext(ctx)
	ErrorContext(ctx)
	LogContext(ctx, InfoLevel)

	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	content := strings.TrimSpace(read())
	if got := strings.Count(content, "\n") + 1; got != 10 {
		t.Errorf("expected 10 records, got %d:\n%s", got, content)
	}
}
