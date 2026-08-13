package session

import (
	"context"
	"fmt"
	"sync"
)

// keyLock serialises work on a single session key.
//
// The session store is read-modify-write: a turn loads the session, appends to
// it, and saves the whole thing back. Manager.mu guards each of those steps
// individually but is released between them, so two turns for the same key that
// overlap in time both load the same prefix and the second save publishes a
// file missing the first turn's messages entirely (#236). Serialising the whole
// load→process→save span is the only ordering that cannot lose a message, and
// it is also what a conversation means: two turns of the same chat are
// sequential by definition, not concurrent.
//
// Locks are reference counted and deleted once idle. The map is keyed by
// session id, and session ids on the API surface come from the client's `user`
// field — an unbounded key space, so a lock that outlived its last holder would
// be a slow memory leak reachable by any authenticated caller.
type keyLock struct {
	mu    sync.Mutex
	locks map[string]*refCountedLock
}

type refCountedLock struct {
	ch   chan struct{} // capacity 1; a token in the channel means "held"
	refs int
}

// wrapCancellation keeps the ErrContextCancelled sentinel — every caller in the
// package matches on it — while preserving which cancellation this was.
//
// A queued turn that exhausts `agents.defaults.timeout` and a client that
// disconnected are the same sentinel, and they call for opposite remedies: the
// first means the operator's timeout is shorter than a turn already in flight
// for that session, the second means nobody is listening. Flattening both to
// "context cancelled" sends the operator hunting for the wrong one.
func wrapCancellation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrContextCancelled, err)
	}
	return ErrContextCancelled
}

func newKeyLock() *keyLock {
	return &keyLock{locks: make(map[string]*refCountedLock)}
}

// acquire blocks until key is free and returns a release function.
//
// It is a channel rather than a sync.Mutex so that waiting stays cancellable:
// a caller whose context expires while queued behind a long turn must get an
// error, not block past its own deadline. sync.Mutex has no such wait.
func (k *keyLock) acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCancellation(ctx)
	}

	k.mu.Lock()
	l, ok := k.locks[key]
	if !ok {
		l = &refCountedLock{ch: make(chan struct{}, 1)}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	// Drop the reference again on every failure path, or a cancelled waiter
	// pins the entry forever and the leak this refcount exists to prevent
	// comes back through the error path.
	release := func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
	}

	select {
	case l.ch <- struct{}{}:
	case <-ctx.Done():
		release()
		return nil, wrapCancellation(ctx)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-l.ch
			release()
		})
	}, nil
}

// LockSession blocks until no other turn in this process is working on
// sessionID, and returns a function that releases it. The release function is
// idempotent, so `defer release()` is safe next to an early explicit call.
//
// Scope: this is a process-local lock. Two joshbot processes sharing one
// sessions directory — the gateway and a concurrent `agent -m` — can still
// interleave, because nothing here takes an OS file lock. That combination is
// rare and was the pre-existing state; the reachable-by-default case is the
// HTTP API, where every request is served by one process.
func (m *Manager) LockSession(ctx context.Context, sessionID string) (func(), error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	return m.keys.acquire(ctx, sessionID)
}
