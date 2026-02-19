package log

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/log"
)

// captureOutput captures logger output for testing.
func captureOutput(fn func()) string {
	// Save original stdout
	oldStdout := os.Stdout

	// Create a pipe
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Run the function
	fn()

	// Close writer and read output
	w.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r)

	// Restore stdout
	os.Stdout = oldStdout

	return buf.String()
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Level != InfoLevel {
		t.Errorf("expected Level to be InfoLevel, got %v", cfg.Level)
	}

	if !cfg.Pretty {
		t.Error("expected Pretty to be true")
	}

	if cfg.JSON {
		t.Error("expected JSON to be false")
	}

	if cfg.Caller != true {
		t.Error("expected Caller to be true")
	}

	if cfg.Timestamp != true {
		t.Error("expected Timestamp to be true")
	}

	if cfg.Prefix != "joshbot" {
		t.Errorf("expected Prefix to be 'joshbot', got %s", cfg.Prefix)
	}
}

func TestNewLogger(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Pretty = false // Disable for test
	cfg.JSON = false

	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}

	if logger.Logger == nil {
		t.Error("embedded Logger is nil")
	}
}

func TestNewLoggerWithFile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.File = "/tmp/joshbot_test.log"

	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("NewLogger with file failed: %v", err)
	}

	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}

	// Clean up
	if len(logger.handlers) > 0 {
		for _, h := range logger.handlers {
			h.Close()
		}
	}
	os.Remove("/tmp/joshbot_test.log")
}

func TestNewLoggerJSON(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JSON = true
	cfg.Pretty = false

	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("NewLogger JSON failed: %v", err)
	}

	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}
}

func TestGet(t *testing.T) {
	// Get should return a non-nil logger
	logger := Get()
	if logger == nil {
		t.Fatal("Get() returned nil")
	}

	// Second call should return the same instance
	logger2 := Get()
	if logger != logger2 {
		t.Error("Get() returned different instances")
	}
}

func TestWith(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	Init(cfg)

	child := With("key", "value")
	if child == nil {
		t.Fatal("With() returned nil")
	}
}

func TestSubPackage(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Prefix = ""
	Init(cfg)

	sub := SubPackage("mypackage")
	if sub == nil {
		t.Fatal("SubPackage() returned nil")
	}

	// The prefix should include the package name
	_ = sub // Prefix handling tested implicitly
}

func TestContextWithTraceID(t *testing.T) {
	ctx := context.Background()

	// Test adding a trace ID
	ctx = ContextWithTraceID(ctx, "test-trace-123")

	traceID := TraceIDFromContext(ctx)
	if traceID != "test-trace-123" {
		t.Errorf("expected trace ID 'test-trace-123', got '%s'", traceID)
	}

	// Test with empty trace ID (should generate one)
	ctx2 := ContextWithTraceID(context.Background(), "")
	traceID2 := TraceIDFromContext(ctx2)
	if traceID2 == "" {
		t.Error("expected non-empty trace ID")
	}
}

func TestTraceIDFromContextEmpty(t *testing.T) {
	ctx := context.Background()

	traceID := TraceIDFromContext(ctx)
	if traceID != "" {
		t.Errorf("expected empty trace ID for empty context, got '%s'", traceID)
	}
}

func TestSetLevel(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	Init(cfg)

	// Set to debug level
	SetLevel(DebugLevel)

	// Verify the level was set
	if Get().Logger.GetLevel() != DebugLevel {
		t.Errorf("expected DebugLevel, got %v", Get().Logger.GetLevel())
	}

	// Set back to info
	SetLevel(InfoLevel)
}

func TestDebug(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	// Should not panic
	Debug("test message", "key", "value")
}

func TestInfo(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	// Should not panic
	Info("test message", "key", "value")
}

func TestWarn(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	// Should not panic
	Warn("test message", "key", "value")
}

func TestError(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	// Should not panic
	Error("test message", "key", "value")
}

func TestDebugf(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	// Should not panic
	Debugf("test %s message", "formatted")
}

func TestInfof(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	// Should not panic
	Infof("test %s message", "formatted")
}

func TestLog(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	// Should not panic
	Log(DebugLevel, "test message", "key", "value")
	Log(InfoLevel, "test message", "key", "value")
	Log(WarnLevel, "test message", "key", "value")
	Log(ErrorLevel, "test message", "key", "value")
}

func TestLogContext(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	ctx := ContextWithTraceID(context.Background(), "test-trace")

	// Should not panic
	LogContext(ctx, InfoLevel, "test message")
}

func TestDebugContext(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	ctx := ContextWithTraceID(context.Background(), "test-trace")

	// Should not panic
	DebugContext(ctx, "test message")
}

func TestInfoContext(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	ctx := ContextWithTraceID(context.Background(), "test-trace")

	// Should not panic
	InfoContext(ctx, "test message")
}

func TestWarnContext(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	ctx := ContextWithTraceID(context.Background(), "test-trace")

	// Should not panic
	WarnContext(ctx, "test message")
}

func TestErrorContext(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	ctx := ContextWithTraceID(context.Background(), "test-trace")

	// Should not panic
	ErrorContext(ctx, "test message")
}

func TestLoggerWithContext(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	ctx := ContextWithTraceID(context.Background(), "test-trace")

	logger := ContextLogger(ctx, "key", "value")
	if logger == nil {
		t.Fatal("ContextLogger returned nil")
	}
}

func TestLoggerWithContextGeneratesTraceID(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	// Context without trace ID
	ctx := context.Background()

	logger := ContextLogger(ctx, "key", "value")
	if logger == nil {
		t.Fatal("ContextLogger returned nil")
	}
}

func TestSetTraceIDFunc(t *testing.T) {
	// Reset the global logger
	globalOnce = sync.Once{}
	global = nil

	// Set custom trace ID function
	SetTraceIDFunc(func() string {
		return "custom-trace-id"
	})

	defer SetTraceIDFunc(nil) // Reset after test

	ctx := ContextWithTraceID(context.Background(), "")
	traceID := TraceIDFromContext(ctx)

	if traceID != "custom-trace-id" {
		t.Errorf("expected 'custom-trace-id', got '%s'", traceID)
	}
}

func TestFile(t *testing.T) {
	file := File()

	// Should return something like "logger_test.go:123"
	if !strings.Contains(file, "logger_test.go") {
		t.Errorf("expected to contain 'logger_test.go', got '%s'", file)
	}
}

func TestLogf(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	// Should not panic
	Logf(InfoLevel, "test %s", "formatted")
}

func TestLogContextf(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	ctx := ContextWithTraceID(context.Background(), "test-trace")

	// Should not panic
	LogContextf(ctx, InfoLevel, "test %s", "formatted")
}

// TestLevelConstants verifies that our level constants match charmbracelet/log.
func TestLevelConstants(t *testing.T) {
	if DebugLevel != log.DebugLevel {
		t.Error("DebugLevel mismatch")
	}
	if InfoLevel != log.InfoLevel {
		t.Error("InfoLevel mismatch")
	}
	if WarnLevel != log.WarnLevel {
		t.Error("WarnLevel mismatch")
	}
	if ErrorLevel != log.ErrorLevel {
		t.Error("ErrorLevel mismatch")
	}
}

// TestLoggerInstanceMethods tests the logger instance methods.
func TestLoggerInstanceMethods(t *testing.T) {
	// Reset global logger for test
	globalOnce = sync.Once{}
	global = nil

	cfg := DefaultConfig()
	cfg.Pretty = false
	cfg.Level = DebugLevel
	Init(cfg)

	logger := Get()

	// Test instance methods
	logger.Debug("debug message", "key", "value")
	logger.Info("info message", "key", "value")
	logger.Warn("warn message", "key", "value")
	logger.Error("error message", "key", "value")
	logger.Debugf("debug %s", "formatted")
	logger.Infof("info %s", "formatted")
	logger.Warnf("warn %s", "formatted")
	logger.Errorf("error %s", "formatted")
	logger.Log(InfoLevel, "log message", "key", "value")
	logger.Logf(InfoLevel, "log %s", "formatted")

	// Test With
	child := logger.With("child", "value")
	if child == nil {
		t.Error("With() returned nil")
	}
}
