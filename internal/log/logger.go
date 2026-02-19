package log

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
)

// Level represents the logging level.
type Level = log.Level

// Log levels matching charmbracelet/log.
const (
	DebugLevel = log.DebugLevel
	InfoLevel  = log.InfoLevel
	WarnLevel  = log.WarnLevel
	ErrorLevel = log.ErrorLevel
)

// Config holds the logger configuration.
type Config struct {
	// Level is the minimum log level to output.
	Level Level

	// Pretty enables pretty-printed output for development.
	Pretty bool

	// JSON enables JSON output for production.
	JSON bool

	// File is the optional file path to write logs to.
	// If empty, logs are written to stdout only.
	File string

	// Caller enables including caller information (file:line).
	Caller bool

	// Timestamp enables including timestamps.
	Timestamp bool

	// Prefix adds a prefix to all log messages.
	Prefix string
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() Config {
	return Config{
		Level:     InfoLevel,
		Pretty:    true,
		JSON:      false,
		File:      "",
		Caller:    true,
		Timestamp: true,
		Prefix:    "joshbot",
	}
}

// contextKey is used to store the trace ID in the context.
type contextKey string

const traceIDKey contextKey = "trace_id"

// Logger is a context-aware structured logger.
// It wraps charmbracelet/log.Logger and provides additional functionality.
type Logger struct {
	*log.Logger
	cfg      Config
	handlers []io.WriteCloser
	mu       sync.RWMutex
}

// Global logger instance.
var (
	global     *Logger
	globalOnce sync.Once
)

// traceIDFunc is an optional function to generate trace IDs.
var traceIDFunc func() string

// SetTraceIDFunc sets the function used to generate trace IDs.
func SetTraceIDFunc(fn func() string) {
	traceIDFunc = fn
}

// Init initializes the global logger with the given configuration.
func Init(cfg Config) error {
	var initErr error
	globalOnce.Do(func() {
		global, initErr = NewLogger(cfg)
	})
	if initErr != nil {
		return initErr
	}
	return nil
}

// NewLogger creates a new Logger with the given configuration.
func NewLogger(cfg Config) (*Logger, error) {
	var writers []io.Writer
	var handlers []io.WriteCloser

	// Always add stdout
	writers = append(writers, os.Stdout)

	// Add file writer if specified
	if cfg.File != "" {
		// Ensure the directory exists
		dir := filepath.Dir(cfg.File)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %w", err)
		}

		file, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
		handlers = append(handlers, file)
		writers = append(writers, file)
	}

	// Create multi-writer
	multiWriter := io.MultiWriter(writers...)

	// Build options
	opts := log.Options{
		Level:           cfg.Level,
		Prefix:          cfg.Prefix,
		ReportCaller:    cfg.Caller,
		ReportTimestamp: cfg.Timestamp,
		TimeFormat:      "2006-01-02 15:04:05",
	}

	if cfg.JSON {
		opts.Formatter = log.JSONFormatter
	}

	// Create the charmbracelet logger with options
	logger := log.NewWithOptions(multiWriter, opts)

	// Configure styles for pretty printing
	if cfg.Pretty && !cfg.JSON {
		styles := log.DefaultStyles()
		styles.Timestamp = lipgloss.NewStyle().Foreground(lipgloss.Color("206"))
		styles.Key = lipgloss.NewStyle().Foreground(lipgloss.Color("219"))
		styles.Value = lipgloss.NewStyle().Foreground(lipgloss.Color("228"))
		styles.Caller = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		styles.Prefix = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
		logger.SetStyles(styles)
	}

	return &Logger{
		Logger:   logger,
		cfg:      cfg,
		handlers: handlers,
	}, nil
}

// Get returns the global logger, creating it with default config if needed.
func Get() *Logger {
	globalOnce.Do(func() {
		if global == nil {
			var err error
			global, err = NewLogger(DefaultConfig())
			if err != nil {
				// Fall back to basic logger
				global = &Logger{
					Logger: log.New(os.Stdout),
					cfg:    DefaultConfig(),
				}
			}
		}
	})
	return global
}

// With creates a child logger with the given key-value pairs.
func With(keyValues ...any) *Logger {
	return &Logger{
		Logger: Get().Logger.With(keyValues...),
		cfg:    Get().cfg,
	}
}

// WithContext creates a logger that includes the trace ID from the context.
func WithContext(ctx context.Context) *Logger {
	traceID, ok := ctx.Value(traceIDKey).(string)
	if !ok || traceID == "" {
		// Generate a new trace ID if none exists
		if traceIDFunc != nil {
			traceID = traceIDFunc()
		} else {
			traceID = generateTraceID()
		}
		ctx = context.WithValue(ctx, traceIDKey, traceID)
	}
	return &Logger{
		Logger: Get().Logger.With("trace_id", traceID),
		cfg:    Get().cfg,
	}
}

// ContextWithTraceID adds a trace ID to the context.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		if traceIDFunc != nil {
			traceID = traceIDFunc()
		} else {
			traceID = generateTraceID()
		}
	}
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceIDFromContext extracts the trace ID from the context.
func TraceIDFromContext(ctx context.Context) string {
	if traceID, ok := ctx.Value(traceIDKey).(string); ok {
		return traceID
	}
	return ""
}

// ContextLogger returns a logger that includes the trace ID from the context.
// This is a convenience method that combines WithContext and With.
func ContextLogger(ctx context.Context, keyValues ...any) *Logger {
	traceID, ok := ctx.Value(traceIDKey).(string)
	if !ok || traceID == "" {
		if traceIDFunc != nil {
			traceID = traceIDFunc()
		} else {
			traceID = generateTraceID()
		}
		keyValues = append([]any{"trace_id", traceID}, keyValues...)
	}
	return &Logger{
		Logger: Get().Logger.With(keyValues...),
		cfg:    Get().cfg,
	}
}

// generateTraceID generates a simple trace ID.
func generateTraceID() string {
	return fmt.Sprintf("trace-%d", <-traceIDChan)
}

// traceIDChan is used to generate unique trace IDs.
var traceIDChan = make(chan int64, 1)

func init() {
	// Seed the trace ID generator
	go func() {
		var i int64
		for {
			traceIDChan <- i
			i++
		}
	}()
}

// Close closes any file handlers. Should be called on shutdown.
func Close() error {
	if global == nil {
		return nil
	}
	for _, h := range global.handlers {
		if h != nil {
			if err := h.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetLevel sets the global log level.
func SetLevel(level Level) {
	Get().Logger.SetLevel(level)
}

// Debug logs a message at debug level.
// If the first argument is a string, it's used as the message.
// Additional arguments are treated as key-value pairs.
func Debug(args ...any) {
	if len(args) == 0 {
		Get().Logger.Debug("")
		return
	}
	Get().Logger.Debug(args[0], args[1:]...)
}

// Debugf logs a formatted message at debug level.
func Debugf(format string, args ...any) {
	Get().Logger.Debugf(format, args...)
}

// Info logs a message at info level.
// If the first argument is a string, it's used as the message.
// Additional arguments are treated as key-value pairs.
func Info(args ...any) {
	if len(args) == 0 {
		Get().Logger.Info("")
		return
	}
	Get().Logger.Info(args[0], args[1:]...)
}

// Infof logs a formatted message at info level.
func Infof(format string, args ...any) {
	Get().Logger.Infof(format, args...)
}

// Warn logs a message at warn level.
// If the first argument is a string, it's used as the message.
// Additional arguments are treated as key-value pairs.
func Warn(args ...any) {
	if len(args) == 0 {
		Get().Logger.Warn("")
		return
	}
	Get().Logger.Warn(args[0], args[1:]...)
}

// Warnf logs a formatted message at warn level.
func Warnf(format string, args ...any) {
	Get().Logger.Warnf(format, args...)
}

// Error logs a message at error level.
// If the first argument is a string, it's used as the message.
// Additional arguments are treated as key-value pairs.
func Error(args ...any) {
	if len(args) == 0 {
		Get().Logger.Error("")
		return
	}
	Get().Logger.Error(args[0], args[1:]...)
}

// Errorf logs a formatted message at error level.
func Errorf(format string, args ...any) {
	Get().Logger.Errorf(format, args...)
}

// Fatal logs a message at fatal level and exits.
// If the first argument is a string, it's used as the message.
// Additional arguments are treated as key-value pairs.
func Fatal(args ...any) {
	if len(args) == 0 {
		Get().Logger.Fatal("")
		return
	}
	Get().Logger.Fatal(args[0], args[1:]...)
}

// Fatalf logs a formatted message at fatal level and exits.
func Fatalf(format string, args ...any) {
	Get().Logger.Fatalf(format, args...)
}

// DebugContext logs a message at debug level with context.
func DebugContext(ctx context.Context, args ...any) {
	if len(args) == 0 {
		ContextLogger(ctx).Logger.Debug("")
		return
	}
	ContextLogger(ctx).Logger.Debug(args[0], args[1:]...)
}

// DebugContextf logs a formatted message at debug level with context.
func DebugContextf(ctx context.Context, format string, args ...any) {
	ContextLogger(ctx).Logger.Debugf(format, args...)
}

// InfoContext logs a message at info level with context.
func InfoContext(ctx context.Context, args ...any) {
	if len(args) == 0 {
		ContextLogger(ctx).Logger.Info("")
		return
	}
	ContextLogger(ctx).Logger.Info(args[0], args[1:]...)
}

// InfoContextf logs a formatted message at info level with context.
func InfoContextf(ctx context.Context, format string, args ...any) {
	ContextLogger(ctx).Logger.Infof(format, args...)
}

// WarnContext logs a message at warn level with context.
func WarnContext(ctx context.Context, args ...any) {
	if len(args) == 0 {
		ContextLogger(ctx).Logger.Warn("")
		return
	}
	ContextLogger(ctx).Logger.Warn(args[0], args[1:]...)
}

// WarnContextf logs a formatted message at warn level with context.
func WarnContextf(ctx context.Context, format string, args ...any) {
	ContextLogger(ctx).Logger.Warnf(format, args...)
}

// ErrorContext logs a message at error level with context.
func ErrorContext(ctx context.Context, args ...any) {
	if len(args) == 0 {
		ContextLogger(ctx).Logger.Error("")
		return
	}
	ContextLogger(ctx).Logger.Error(args[0], args[1:]...)
}

// ErrorContextf logs a formatted message at error level with context.
func ErrorContextf(ctx context.Context, format string, args ...any) {
	ContextLogger(ctx).Logger.Errorf(format, args...)
}

// FatalContext logs a message at fatal level with context and exits.
func FatalContext(ctx context.Context, args ...any) {
	if len(args) == 0 {
		ContextLogger(ctx).Logger.Fatal("")
		return
	}
	ContextLogger(ctx).Logger.Fatal(args[0], args[1:]...)
}

// FatalContextf logs a formatted message at fatal level with context and exits.
func FatalContextf(ctx context.Context, format string, args ...any) {
	ContextLogger(ctx).Logger.Fatalf(format, args...)
}

// Log is a convenience method that logs at the specified level.
func Log(level Level, args ...any) {
	if len(args) == 0 {
		Get().Logger.Log(level, "")
		return
	}
	Get().Logger.Log(level, args[0], args[1:]...)
}

// Logf is a convenience method that logs a formatted message at the specified level.
func Logf(level Level, format string, args ...any) {
	Get().Logger.Logf(level, format, args...)
}

// LogContext is a convenience method that logs at the specified level with context.
func LogContext(ctx context.Context, level Level, args ...any) {
	if len(args) == 0 {
		ContextLogger(ctx).Logger.Log(level, "")
		return
	}
	ContextLogger(ctx).Logger.Log(level, args[0], args[1:]...)
}

// LogContextf is a convenience method that logs a formatted message at the specified level with context.
func LogContextf(ctx context.Context, level Level, format string, args ...any) {
	ContextLogger(ctx).Logger.Logf(level, format, args...)
}

// SubPackage creates a logger with an additional prefix for the given package.
func SubPackage(pkg string) *Logger {
	return &Logger{
		Logger: Get().Logger.WithPrefix(pkg),
		cfg:    Get().cfg,
	}
}

// File returns the file name and line number of the caller.
func File() string {
	_, file, line, ok := runtime.Caller(1)
	if !ok {
		return "?:0"
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

// ---- Logger instance methods ----

// Debug logs a message at debug level.
func (l *Logger) Debug(args ...any) {
	if len(args) == 0 {
		l.Logger.Debug("")
		return
	}
	l.Logger.Debug(args[0], args[1:]...)
}

// Debugf logs a formatted message at debug level.
func (l *Logger) Debugf(format string, args ...any) {
	l.Logger.Debugf(format, args...)
}

// Info logs a message at info level.
func (l *Logger) Info(args ...any) {
	if len(args) == 0 {
		l.Logger.Info("")
		return
	}
	l.Logger.Info(args[0], args[1:]...)
}

// Infof logs a formatted message at info level.
func (l *Logger) Infof(format string, args ...any) {
	l.Logger.Infof(format, args...)
}

// Warn logs a message at warn level.
func (l *Logger) Warn(args ...any) {
	if len(args) == 0 {
		l.Logger.Warn("")
		return
	}
	l.Logger.Warn(args[0], args[1:]...)
}

// Warnf logs a formatted message at warn level.
func (l *Logger) Warnf(format string, args ...any) {
	l.Logger.Warnf(format, args...)
}

// Error logs a message at error level.
func (l *Logger) Error(args ...any) {
	if len(args) == 0 {
		l.Logger.Error("")
		return
	}
	l.Logger.Error(args[0], args[1:]...)
}

// Errorf logs a formatted message at error level.
func (l *Logger) Errorf(format string, args ...any) {
	l.Logger.Errorf(format, args...)
}

// Fatal logs a message at fatal level and exits.
func (l *Logger) Fatal(args ...any) {
	if len(args) == 0 {
		l.Logger.Fatal("")
		return
	}
	l.Logger.Fatal(args[0], args[1:]...)
}

// Fatalf logs a formatted message at fatal level and exits.
func (l *Logger) Fatalf(format string, args ...any) {
	l.Logger.Fatalf(format, args...)
}

// Log logs a message at the specified level.
func (l *Logger) Log(level Level, args ...any) {
	if len(args) == 0 {
		l.Logger.Log(level, "")
		return
	}
	l.Logger.Log(level, args[0], args[1:]...)
}

// Logf logs a formatted message at the specified level.
func (l *Logger) Logf(level Level, format string, args ...any) {
	l.Logger.Logf(level, format, args...)
}

// With creates a child logger with the given key-value pairs.
func (l *Logger) With(keyValues ...any) *Logger {
	return &Logger{
		Logger: l.Logger.With(keyValues...),
		cfg:    l.cfg,
	}
}
