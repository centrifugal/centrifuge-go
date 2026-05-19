package centrifuge

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func assertTrue(t *testing.T, condition bool, msg string) {
	if !condition {
		t.Fatalf("Assertion failed: %s", msg)
	}
}

func assertEqual(t *testing.T, expected, actual interface{}, msg string) {
	if expected != actual {
		t.Fatalf("Assertion failed: %s - expected: %v, got: %v", msg, expected, actual)
	}
}

func newTestQueue() *cbQueue {
	return &cbQueue{
		closeCh: make(chan struct{}),
		notify:  make(chan struct{}, 1),
	}
}

func TestCbQueue_PushAndDispatch(t *testing.T) {
	q := newTestQueue()

	var wg sync.WaitGroup
	wg.Add(1)

	// Start the dispatcher in a separate goroutine.
	go q.dispatch()

	startTime := time.Now()
	q.push(func(d time.Duration) {
		defer wg.Done()
		assertTrue(t, d >= 0, "Callback duration should be positive")
	})

	// Wait for the callback to finish.
	wg.Wait()

	// Ensure the callback executed quickly.
	elapsed := time.Since(startTime)
	assertTrue(t, elapsed < 100*time.Millisecond, "Callback should be dispatched immediately")
}

func TestCbQueue_OrderPreservation(t *testing.T) {
	q := newTestQueue()

	// Start the dispatcher in a separate goroutine.
	go q.dispatch()

	var results []int
	var mu sync.Mutex
	expectedResults := []int{1, 2, 3}

	for _, i := range expectedResults {
		i := i
		q.push(func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			results = append(results, i)
		})
	}

	// Allow time for the queue to process.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	for i, r := range results {
		assertEqual(t, expectedResults[i], r, "unexpected result")
	}
}

func TestCbQueue_Close(t *testing.T) {
	q := newTestQueue()

	go q.dispatch()

	var executed bool
	q.push(func(d time.Duration) {
		executed = true
	})

	q.close()

	// Ensure the closeCh channel is closed.
	select {
	case <-q.closeCh:
		// Channel was closed as expected.
	case <-time.After(1 * time.Second):
		t.Fatal("closeCh was not closed after queue close")
	}

	assertTrue(t, executed, "Callback should be executed before close")
}

func TestCbQueue_IgnorePushAfterClose(t *testing.T) {
	q := newTestQueue()
	go q.dispatch()
	q.close()

	var executed bool
	q.push(func(d time.Duration) {
		executed = true
	})

	// Allow some time to see if the callback is executed.
	time.Sleep(100 * time.Millisecond)

	assertTrue(t, !executed, "Callback should not be executed after queue close")
}

func TestCbQueue_PushNilCallbackPanics(t *testing.T) {
	q := newTestQueue()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Expected panic when pushing nil callback with close set to false")
		}
	}()

	q.pushOrClose(nil, false)
}

// TestCbQueue_SignalNotLostBetweenUnlockAndWait exercises the window that
// differs from the old sync.Cond implementation: in the new code there is a
// brief gap between releasing the mutex and blocking on the notify channel
// where a push can arrive. The buffered channel must absorb that signal so
// dispatch does not miss it and stall.
//
// We force the race by making the in-flight callback hold up dispatch while
// we enqueue the next item, so dispatch will definitely reach the channel-wait
// with an already-buffered signal rather than an empty channel.
func TestCbQueue_SignalNotLostBetweenUnlockAndWait(t *testing.T) {
	q := newTestQueue()
	go q.dispatch()

	// Block dispatch inside the first callback until we have pushed the
	// second item, ensuring the notify signal is sent before dispatch can
	// loop back and check the queue.
	secondPushed := make(chan struct{})
	firstDone := make(chan struct{})
	secondDone := make(chan struct{})

	q.push(func(_ time.Duration) {
		// Signal that the second item has been enqueued, then wait until
		// the test confirms it before returning — this keeps dispatch busy
		// so it will hit the channel-wait with the signal already buffered.
		<-secondPushed
		close(firstDone)
	})

	q.push(func(_ time.Duration) {
		close(secondDone)
	})
	close(secondPushed) // let the first callback return

	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second callback was never dispatched; notify signal may have been lost")
	}
	<-firstDone
}

// TestCbQueue_DrainBeforeClose verifies that all callbacks already in the queue
// are executed before close() returns — the backwards-compatibility guarantee.
func TestCbQueue_DrainBeforeClose(t *testing.T) {
	q := newTestQueue()
	go q.dispatch()

	const n = 100
	var count atomic.Int32
	for i := 0; i < n; i++ {
		q.push(func(_ time.Duration) {
			count.Add(1)
		})
	}

	q.close()

	if got := count.Load(); got != n {
		t.Fatalf("expected %d callbacks executed before close, got %d", n, got)
	}
}

// TestCbQueue_CloseEmptyQueue verifies that close() on an idle queue
// completes promptly without hanging.
func TestCbQueue_CloseEmptyQueue(t *testing.T) {
	q := newTestQueue()
	go q.dispatch()

	done := make(chan struct{})
	go func() {
		q.close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close() hung on empty queue")
	}
}

// TestCbQueue_ConcurrentPushers verifies that callbacks from many concurrent
// goroutines are all dispatched exactly once.
func TestCbQueue_ConcurrentPushers(t *testing.T) {
	q := newTestQueue()
	go q.dispatch()

	const goroutines = 10
	const perGoroutine = 50
	var count atomic.Int32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				q.push(func(_ time.Duration) {
					count.Add(1)
				})
			}
		}()
	}
	wg.Wait()
	q.close()

	if got := count.Load(); got != goroutines*perGoroutine {
		t.Fatalf("expected %d callbacks, got %d", goroutines*perGoroutine, got)
	}
}

// TestCbQueue_SignalNotLostUnderRapidPush pushes many items without any pause,
// exercising the path where multiple pushes share a single buffered signal and
// dispatch must drain the entire queue before waiting again.
func TestCbQueue_SignalNotLostUnderRapidPush(t *testing.T) {
	q := newTestQueue()
	go q.dispatch()

	const n = 1000
	var count atomic.Int32
	for i := 0; i < n; i++ {
		q.push(func(_ time.Duration) {
			count.Add(1)
		})
	}
	q.close()

	if got := count.Load(); got != n {
		t.Fatalf("expected %d callbacks, got %d", n, got)
	}
}

// TestCbQueue_CallbacksRunSerially verifies that dispatch never executes two
// callbacks concurrently — a property callers depend on for ordering.
func TestCbQueue_CallbacksRunSerially(t *testing.T) {
	q := newTestQueue()
	go q.dispatch()

	const n = 200
	var active atomic.Int32
	var overlap bool
	var mu sync.Mutex

	for i := 0; i < n; i++ {
		q.push(func(_ time.Duration) {
			if active.Add(1) > 1 {
				mu.Lock()
				overlap = true
				mu.Unlock()
			}
			time.Sleep(time.Microsecond) // give scheduler a chance to interleave
			active.Add(-1)
		})
	}
	q.close()

	mu.Lock()
	defer mu.Unlock()
	if overlap {
		t.Fatal("two callbacks ran concurrently — dispatch is not serial")
	}
}
