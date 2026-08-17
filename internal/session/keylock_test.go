package session

import (
	"context"
	"errors"
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

// TestLockSessionRejectsOverflowPerKey is the deterministic regression test for
// #245: a flood on one session key must not queue an unbounded number of turns.
//
// The current turn holds the key for the whole test; every later turn for the
// key passes the cap gate only while the running ref count is below
// MaxConcurrentTurnsPerKey, after which acquire returns ErrKeyLockBusy at once —
// no ref, no wait. Because the gate and the increment are both under the lock's
// mutex, exactly MaxConcurrentTurnsPerKey-1 of the overflow turns are admitted
// (the cap-th holds the token, the rest park behind it) and the remainder are
// rejected, so the busy count is exact:
//
//	busy == num - (MaxConcurrentTurnsPerKey - 1)
//
// With no cap every overflow turn is admitted and merely blocks on the held token
// until its context expires, so busy would land at 0 and the final guard fails —
// the test that would have been red before the fix landed.

func TestLockSessionRejectsOverflowPerKey(t *testing.T) {
	m := newTestManager(t)

	// The current turn keeps the key for the whole test.
	hold, err := m.LockSession(context.Background(), "cli:flood")
	if err != nil {
		t.Fatalf("LockSession(hold): %v", err)
	}
	defer hold()

	const num = MaxConcurrentTurnsPerKey + 5 // five turns past the cap
	wantBusy := num - (MaxConcurrentTurnsPerKey - 1)

	// A short deadline so the turns that DO pass the gate — they park behind the
	// held token — return when the deadline fires instead of blocking the whole
	// test. The rejected turns return ErrKeyLockBusy at once, which fixes the count.
	results := make(chan error, num)
	var wg sync.WaitGroup
	for i := 0; i < num; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			_, err := m.LockSession(ctx, "cli:flood")
			// A nil err means an overflow turn acquired the held key, which is wrong.
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	busy, leaked := 0, 0
	for err := range results {
		switch {
		case err == nil:
			leaked++
		case errors.Is(err, ErrKeyLockBusy):
			busy++
		}
	}
	if busy != wantBusy {
		t.Errorf("rejected %d of %d overflow turns as busy, want %d (cap=%d)",
			busy, num, wantBusy, MaxConcurrentTurnsPerKey)
	}
	if leaked != 0 {
		t.Errorf("%d overflow turn(s) acquired a key held by the current turn", leaked)
	}
	if busy == 0 {
		t.Fatal("a saturated key accepted the entire overflow; the per-key cap is not engaging (#245)")
	}
}
