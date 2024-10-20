package centrifuge

import (
	"sync"
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
	q := &cbQueue{
		closeCh: make(chan struct{}),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
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
