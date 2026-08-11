package main

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// blockingReader never yields data, modelling a terminal sitting at a prompt.
type blockingReader struct{ release chan struct{} }

func newBlockingReader() *blockingReader {
	return &blockingReader{release: make(chan struct{})}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

// noopAgent stands in for the real agent; the loop under test must never
// reach it in these cases.
type noopAgent struct{}

func (noopAgent) Process(context.Context, bus.InboundMessage) (string, error) {
	return "", nil
}

// discardWriter is a trivially safe io.Writer for tests.
type discardWriter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (w *discardWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// The interactive loop must abandon a blocked read when shutdown is signalled.
// Previously the loop checked `done` only between reads, so a SIGINT arriving
// while it sat at the prompt was never observed and the process could only be
// killed with SIGKILL (issue #104).
func TestRunAgentLoop_ReturnsWhenDoneClosesDuringBlockedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	reader := newBlockingReader()
	out := &discardWriter{}

	returned := make(chan error, 1)
	go func() {
		returned <- runAgentLoop(ctx, cancel, done, reader, out, noopAgent{}, nil, false)
	}()

	// Let it reach the blocking read.
	time.Sleep(200 * time.Millisecond)
	close(done)

	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("runAgentLoop returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runAgentLoop did not return after shutdown was signalled — " +
			"the process would be unkillable except by SIGKILL")
	}
}

// Cancelling the context must also unblock the loop.
func TestRunAgentLoop_ReturnsWhenContextCancelledDuringBlockedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer close(done)

	reader := newBlockingReader()
	out := &discardWriter{}

	returned := make(chan error, 1)
	go func() {
		returned <- runAgentLoop(ctx, cancel, done, reader, out, noopAgent{}, nil, false)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("runAgentLoop ignored context cancellation while blocked on input")
	}
}
