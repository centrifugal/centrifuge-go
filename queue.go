package centrifuge

import (
	"bytes"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// cbQueue allows processing callbacks in separate goroutine with
// preserved order.
// This queue implementation is a slightly modified code borrowed from
// https://github.com/nats-io/nats.go client released under Apache 2.0
// license: see https://github.com/nats-io/nats.go/blob/master/LICENSE.
type cbQueue struct {
	mu      sync.Mutex
	notify  chan struct{}
	head    *asyncCB
	tail    *asyncCB
	closeCh chan struct{}
	closed  bool
	// dispatchGoroutine is the id of the goroutine running dispatch, see
	// inDispatch.
	dispatchGoroutine atomic.Uint64
}

type asyncCB struct {
	fn   func(delay time.Duration)
	tm   time.Time
	next *asyncCB
}

// dispatch is responsible for calling async callbacks. Should be run
// in separate goroutine.
func (q *cbQueue) dispatch() {
	q.dispatchGoroutine.Store(goroutineID())
	for {
		q.mu.Lock()
		curr := q.head
		if curr != nil {
			q.head = curr.next
			if curr == q.tail {
				q.tail = nil
			}
		}
		q.mu.Unlock()

		if curr == nil {
			<-q.notify
			continue
		}

		// This signals that the dispatcher has been closed and all
		// previous callbacks have been dispatched.
		if curr.fn == nil {
			close(q.closeCh)
			return
		}
		curr.fn(time.Since(curr.tm))
	}
}

// Push adds the given function to the tail of the list and
// signals the dispatcher. It returns false when the queue is
// closed and the function won't run.
func (q *cbQueue) push(f func(duration time.Duration)) bool {
	return q.pushOrClose(f, false)
}

// Close signals that async queue must be closed.
// Queue won't accept any more callbacks after that – ignoring them if pushed.
// It waits until the callbacks pushed before have run, except when called
// from a callback, which would wait for itself.
func (q *cbQueue) close() {
	q.pushOrClose(nil, true)
	if q.inDispatch() {
		return
	}
	q.waitClose()
}

func (q *cbQueue) waitClose() {
	<-q.closeCh
}

// inDispatch reports whether the caller runs on the dispatcher goroutine,
// i.e. inside a callback. It parses the goroutine's stack header, so keep it
// off paths that run for every callback.
func (q *cbQueue) inDispatch() bool {
	id := q.dispatchGoroutine.Load()
	return id != 0 && id == goroutineID()
}

// goroutineID returns the id of the calling goroutine, parsed from the header
// of its stack trace ("goroutine 18 [running]:"). Go has no API for it.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	b := bytes.TrimPrefix(buf[:n], []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		if id, err := strconv.ParseUint(string(b[:i]), 10, 64); err == nil {
			return id
		}
	}
	return 0
}

func (q *cbQueue) pushOrClose(f func(time.Duration), close bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	// Make sure that library is not calling push with nil function,
	// since this is used to notify the dispatcher that it must stop.
	if !close && f == nil {
		panic("pushing a nil callback with false close")
	}
	cb := &asyncCB{fn: f, tm: time.Now()}
	if q.tail != nil {
		q.tail.next = cb
	} else {
		q.head = cb
	}
	q.tail = cb
	if close {
		q.closed = true
	}
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return true
}
