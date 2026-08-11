package mcp

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestConnectRetryRacesWithInFlightCalls is the regression test for the
// stdin/cmd data race. A failed handshake leaves started=false, so Connect is a
// supported retry path; it used to publish the new process's stdin under
// startMu while writeMessage read the same field under writeM, two different
// mutexes. Under -race that is a reported write/read pair on c.stdin (and,
// worse, concurrent use of an os.File whose pipe the previous process already
// closed). The fix snapshots the writer and the done channel together as one
// procSession, so a request and the wait for its process cannot come from
// different processes.
//
// Run with -race; without it this test only proves nothing deadlocks.
func TestConnectRetryRacesWithInFlightCalls(t *testing.T) {
	// "nohandshake" exits before answering initialize, so every Connect fails
	// and every retry allocates a fresh process, pipe and done channel.
	c := helperClient(t, "nohandshake")
	t.Cleanup(func() { _ = c.Close() })

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Reconnect attempts: each one swaps the session underneath any in-flight
	// call.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			_ = c.Connect(ctx)
			cancel()
			select {
			case <-stop:
				return
			default:
			}
		}
	}()

	// Callers writing to whatever session is current. Errors are expected and
	// irrelevant; the race detector is the assertion.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				_, _ = c.ListTools(ctx)
				cancel()
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}
