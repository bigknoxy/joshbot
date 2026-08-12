package subagent

import (
	"context"
	"fmt"
	"sync"
)

// Handle tracks a subagent started in the background.
//
// A background run outlives the call that started it, so the two ways of
// learning it finished are both here: Wait blocks, and the notify callback
// passed to RunBackground fires exactly once. The callback is what makes
// background execution useful from a chat channel, where there is nobody to
// block.
type Handle struct {
	done   chan struct{}
	cancel context.CancelFunc

	mu  sync.Mutex
	res *SubResult
	err error
}

// Done is closed when the run has finished, one way or another.
func (h *Handle) Done() <-chan struct{} { return h.done }

// Cancel stops the run. Wait then returns the cancellation error. Calling it
// after completion is a no-op.
func (h *Handle) Cancel() {
	if h.cancel != nil {
		h.cancel()
	}
}

// Result reports the outcome without blocking. ok is false while the run is
// still going, so a caller polling cannot mistake a zero result for an answer.
func (h *Handle) Result() (res *SubResult, err error, ok bool) {
	select {
	case <-h.done:
	default:
		return nil, nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.res, h.err, true
}

// Wait blocks until the run finishes or ctx is done. A ctx that expires first
// returns its error and leaves the run going — use Cancel to stop it.
func (h *Handle) Wait(ctx context.Context) (*SubResult, error) {
	select {
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.res, h.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RunBackground starts a subagent and returns immediately.
//
// The run gets its own cancellable context derived from ctx, so the caller's
// context still governs it but Cancel works independently. notify (which may
// be nil) is called once with the outcome after the handle is settled, so a
// callback that inspects the handle sees the final result rather than racing
// it. A panic inside the run is recovered into the error: a background
// goroutine that panics takes the whole process down, which is a poor trade
// for one failed delegation.
func (r *Runner) RunBackground(ctx context.Context, prompt string, cfg Config, notify func(*SubResult, error)) *Handle {
	runCtx, cancel := context.WithCancel(ctx)
	h := &Handle{done: make(chan struct{}), cancel: cancel}

	go func() {
		defer cancel()
		var res *SubResult
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("subagent panicked: %v", p)
					res = nil
				}
			}()
			res, err = r.Run(runCtx, prompt, cfg)
		}()

		// Run reports a cancelled context as a TimedOut result with a nil
		// error, which is right for a chat turn that should still show its
		// partial answer. A background handle is different: nobody is reading
		// partial output, and a Cancel that returns success reads as an answer.
		if err == nil && runCtx.Err() != nil {
			err = runCtx.Err()
			res = nil
		}

		h.mu.Lock()
		h.res, h.err = res, err
		h.mu.Unlock()
		close(h.done)

		if notify != nil {
			notify(res, err)
		}
	}()

	return h
}
