package mutex

import (
	"context"
	"sync"
)

// Mutex is a drop-in replacement for the standard libraries sync.Mutex. It
// offers the ability cancel waiting to obtain a lock by making use of Go's
// select statement.
//
// The zero value is safe to use.
type Mutex struct {
	initMu sync.Mutex

	// state must be a buffered mmutex of 1.
	// locked: len(state) == 1
	// unlocked: len(state) == 0
	state chan struct{}
}

// init is used to fix a zero value Mutex.
func (m *Mutex) init() {
	m.initMu.Lock()
	defer m.initMu.Unlock()
	if m.state == nil {
		m.state = make(chan struct{}, 1)
	}
}

func (m *Mutex) waitLock() chan<- struct{} {
	m.init()
	return m.state
}

// WaitLock give access to internal state of the mutex and a send chan. do not
// close the chan, it will be cleaned up by gc. closing the channel will break
// the Mutex. Only use the chan it to send en empty struct to obtain the lock.
func (m *Mutex) WaitLock() chan<- struct{} {
	return m.waitLock()
}

// LockCtx is a convience func for selecting WaitLock and ctx. If the ctx is
// done the ctx error will be returned.
func (m *Mutex) LockCtx(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.waitLock() <- struct{}{}:
		return nil
	}
}

// Lock locks m. If the lock is already in use, the calling goroutine blocks
// until the mutex is available.
func (m *Mutex) Lock() {
	m.waitLock() <- struct{}{}
}

// TryLock tries to lock m and reports whether it succeeded.
//
// Note that while correct uses of TryLock do exist, they are rare, and use of
// TryLock is often a sign of a deeper problem in a particular use of mutexes.
func (m *Mutex) TryLock() bool {
	select {
	case m.waitLock() <- struct{}{}:
		return true
	default:
		return false
	}
}

// Unlock unlocks m. It is a run-time error if m is not locked on entry to
// Unlock.
//
// Calling unlock on an unlocked lock is generally an indication of a race
// condition.
func (m *Mutex) Unlock() {
	m.init()
	select {
	case <-m.state:
	default:
		panic("unlock of unlocked mutex")
	}
}
