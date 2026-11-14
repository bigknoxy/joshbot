package tools

import (
	"context"
	"time"
)

// SpillConfig holds configuration for the spill sink.
// Threshold is the maximum result size in bytes before spilling occurs.
// A value of 0 means spilling is disabled (default-off behavior).
//
// MaxSize is the maximum size in bytes for a single spill file. Results
// larger than this will be truncated to MaxSize with head/tail preserved.
// Zero means no per-file cap (only Threshold applies).
//
// MaxAge is the maximum age of spill files before they are pruned. Zero
// means no age-based pruning.
//
// Dir is where spilled results are written. Empty means the sink derives a
// location from the workspace on the context.
type SpillConfig struct {
	Threshold int
	MaxSize   int
	MaxAge    time.Duration
	Dir       string
}

// spillKey is the unexported context key for SpillSink.
type spillKey struct{}

// SpillSinkFromContext returns the SpillConfig from the tool execution context.
// When no spill config is attached (the common case), it returns a zero-value
// config with Threshold==0, which means spilling is disabled and output is
// byte-identical to today's behaviour.
func SpillSinkFromContext(ctx context.Context) SpillConfig {
	if ctx == nil {
		return SpillConfig{}
	}
	if cfg, ok := ctx.Value(spillKey{}).(SpillConfig); ok {
		return cfg
	}
	return SpillConfig{}
}

// WithSpillSink attaches a SpillConfig to the context for the current tool
// execution. Passing a zero-value config effectively disables spilling, which
// is the default-off behaviour required by this issue.
func WithSpillSink(ctx context.Context, cfg SpillConfig) context.Context {
	return context.WithValue(ctx, spillKey{}, cfg)
}
