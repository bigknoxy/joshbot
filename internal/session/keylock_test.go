package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestLockSessionSerialisesTheSameKey(t *testing.T) {
	m := newTestManager(t)

	var mu sync.Mutex
	inside, maxInside := 0, 0

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := m.LockSession(context.Background(), "cli:user")
			if err != nil {
				t.Errorf("LockSession: %v", err)
				return
			}
			defer release()

			mu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			mu.Unlock()

			time.Sleep(2 * time.Millisecond)

			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if maxInside != 1 {
		t.Errorf("%d holders were inside the lock at once, want 1", maxInside)
	}
}

func TestLockSessionDoesNotSerialiseDifferentKeys(t *testing.T) {
	m := newTestManager(t)

	// Hold one key, then take a different one. If the lock were global rather
	// than per-key this would block until the deadline and fail.
	releaseA, err := m.LockSession(context.Background(), "cli:a")
	if err != nil {
		t.Fatalf("LockSession(a): %v", err)
	}
	defer releaseA()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	releaseB, err := m.LockSession(ctx, "cli:b")
	if err != nil {
		t.Fatalf("LockSession(b) blocked behind an unrelated key: %v", err)
	}
	releaseB()
}

// A caller queued behind a long turn must fail on its own deadline rather than
// wait past it. This is why the lock is a channel and not a sync.Mutex, which
// has no cancellable wait.
func TestLockSessionWaitIsCancellable(t *testing.T) {
	m := newTestManager(t)

	release, err := m.LockSession(context.Background(), "cli:user")
	if err != nil {
		t.Fatalf("LockSession: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := m.LockSession(ctx, "cli:user")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a second holder acquired a held lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LockSession ignored its context deadline")
	}
}

// The lock map is keyed by session id, and session ids on the API surface come
// from the client's `user` field — an unbounded key space. An entry that
// outlived its last holder would be a memory leak any authenticated caller
// could grow, so every path must drop its reference, including the one that
// gives up waiting.
func TestLockMapDrainsIncludingCancelledWaiters(t *testing.T) {
	m := newTestManager(t)

	for i := 0; i < 20; i++ {
		release, err := m.LockSession(context.Background(), "cli:user")
		if err != nil {
			t.Fatalf("LockSession: %v", err)
		}
		release()
	}

	held, err := m.LockSession(context.Background(), "cli:held")
	if err != nil {
		t.Fatalf("LockSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := m.LockSession(ctx, "cli:held"); err == nil {
		t.Fatal("expected the queued waiter to time out")
	}
	held()

	m.keys.mu.Lock()
	n := len(m.keys.locks)
	m.keys.mu.Unlock()
	if n != 0 {
		t.Errorf("lock map holds %d idle entries, want 0", n)
	}
}

// `defer release()` next to an early explicit release must not corrupt the
// lock. A second `<-l.ch` would steal the token from whoever holds it next.
func TestReleaseIsIdempotent(t *testing.T) {
	m := newTestManager(t)

	release, err := m.LockSession(context.Background(), "cli:user")
	if err != nil {
		t.Fatalf("LockSession: %v", err)
	}
	release()
	release()

	next, err := m.LockSession(context.Background(), "cli:user")
	if err != nil {
		t.Fatalf("LockSession after double release: %v", err)
	}
	next()

	m.keys.mu.Lock()
	n := len(m.keys.locks)
	m.keys.mu.Unlock()
	if n != 0 {
		t.Errorf("lock map holds %d idle entries after double release, want 0", n)
	}
}

func TestLockSessionRejectsAnInvalidID(t *testing.T) {
	m := newTestManager(t)

	if _, err := m.LockSession(context.Background(), "../escape"); err == nil {
		t.Error("LockSession accepted a traversal-shaped session id")
	}
}
