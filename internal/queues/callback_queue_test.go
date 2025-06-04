package queues

import (
	"context"
	"errors"
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

func assertNoError(t *testing.T, err error, msg string) {
	if err != nil {
		t.Fatalf("Assertion failed: %s - error: %v", msg, err)
	}
}

func assertErrorIs(t *testing.T, err error, target error, msg string) {
	if err == nil {
		t.Fatalf("Assertion failed: %s - expected error, got nil", msg)
	}
	if !errors.Is(err, target) {
		t.Fatalf("Assertion failed: %s - expected %v, got: %v", msg, target, err)
	}
}

func TestCallbackQueue_newUnopenedCallBackQueue(t *testing.T) {
	q := newUnopenedCallBackQueue()
	assertTrue(t, q != nil, "newUnopenedCallBackQueue should return a non-nil queue")
	assertTrue(t, !q.opened.Load(), "newUnopenedCallBackQueue should return a closed queue")
	assertTrue(t, q.list.Len() == 0, "newUnopenedCallBackQueue should return an empty queue")
	// check that the enqueueSignals channel is buffered
	select {
	case q.enqueueSignals <- struct{}{}:
	default:
		t.Fatal("enqueueSignals is not a buffered channel")
	}
}

func TestCallbackQueue_OpenCallBackQueue(t *testing.T) {
	q := OpenCallBackQueue()
	assertTrue(t, q != nil, "OpenCallBackQueue should return a non-nil queue")
	assertTrue(t, q.opened.Load(), "OpenCallBackQueue should return an opened queue")
	q.Close()
	assertTrue(t, !q.opened.Load(), "OpenCallBackQueue should close the queue after Close() is called")
	// check that the queue was closed by acquiring the running lock.
	q.running.Lock()
	defer q.running.Unlock()
}

func TestCallbackQueue_processCallBacks(t *testing.T) {
	q := newUnopenedCallBackQueue()
	// stage a callback to be processed
	cbStarted := make(chan struct{})
	cbFinished := make(chan struct{})
	cb := func(ctx context.Context, d time.Duration) {
		close(cbStarted)
		<-ctx.Done()
		close(cbFinished)
	}
	q.list.PushBack(&callBackRequest{fn: cb, tm: time.Now()})
	// run the processCallBacks method
	go q.processCallBacks()
	<-cbStarted // wait for the callback to start processing
	assertTrue(t, q.list.Len() == 0, "Callback queue should be empty after processing")
	close(q.closeSignal)
	// the context was canceled when closeSignal was closed.
	<-cbFinished
}

func TestCallbackQueue_processCallBacks_auto_dequeues(t *testing.T) {
	q := newUnopenedCallBackQueue()
	go q.processCallBacks()
	// stage a callback to be processed
	n := 10
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		cb := func(ctx context.Context, d time.Duration) {
			defer wg.Done()
		}
		q.list.PushBack(&callBackRequest{fn: cb, tm: time.Now()})
	}
	// only send one signal, the processCallBacks should dequeue the rest.
	q.signalEnqueue()
	wg.Wait()
	assertTrue(t, q.list.Len() == 0, "Callback queue should be empty after processing all callbacks")
}

func TestCallbackQueue_Push_does_not_block(t *testing.T) {
	q := newUnopenedCallBackQueue()
	defer q.list.Clear()
	// Open the queue to allow pushing callbacks.
	q.opened.Store(true)
	neverProcessed := func(ctx context.Context, d time.Duration) {}
	n := 100
	for range n {
		// push callbacks while there is nothing to dequeue.
		err := q.Push(neverProcessed)
		assertNoError(t, err, "Push should not return an error")
	}
	assertEqual(t, q.list.Len(), n, "Queue should have n callbacks queued")
	assertEqual(t, len(q.enqueueSignals), 1, "enqueueSignals should not have any signals")
}

func TestCallbackQueue_Push_processed(t *testing.T) {
	q := OpenCallBackQueue()
	defer q.Close()

	var wg sync.WaitGroup
	wg.Add(1)

	startTime := time.Now()
	cb := func(ctx context.Context, d time.Duration) {
		defer wg.Done()
		assertTrue(t, d >= 0, "Callback duration should be positive")
	}
	err := q.Push(cb)
	assertNoError(t, err, "Push should not return an error")

	// Wait for the callback to finish.
	wg.Wait()

	// Ensure the callback executed quickly.
	elapsed := time.Since(startTime)
	assertTrue(t, elapsed < 100*time.Millisecond, "Callback should be dispatched immediately")
}

func TestCallbackQueue_Push_order_preserved(t *testing.T) {
	q := OpenCallBackQueue()
	defer q.Close()
	expectedResults := []string{"a", "b", "c"}
	var wg sync.WaitGroup
	wg.Add(len(expectedResults))
	// Process callbacks.
	results := make([]string, len(expectedResults))
	for i, v := range expectedResults {
		i, v := i, v // Capture loop variables
		err := q.Push(func(_ context.Context, _ time.Duration) {
			defer wg.Done()
			results[i] = v
		})
		assertTrue(t, err == nil, "Push should not return an error")
	}
	wg.Wait()
	// Check order.
	for i, v := range results {
		assertEqual(t, expectedResults[i], v, "unexpected result")
	}
}

func TestCallbackQueue_Close(t *testing.T) {
	q := OpenCallBackQueue()
	var executed bool
	err := q.Push(func(_ context.Context, _ time.Duration) {
		executed = true
	})
	q.Close()
	assertNoError(t, err, "Push should not return an error")
	assertTrue(t, executed, "Callback should be executed before close")
}

func TestCallbackQueue_IgnorePushAfterClose(t *testing.T) {
	q := OpenCallBackQueue()
	q.Close()
	var executed bool
	err := q.Push(func(_ context.Context, _ time.Duration) {
		executed = true
	})
	assertErrorIs(t, err, ErrQueueClosed, "Push should return an error after queue close")
	assertTrue(t, !executed, "Callback should not be executed after queue close")
}

func TestCallbackQueue_Push_unopened_returns_ErrClosed(t *testing.T) {
	q := newUnopenedCallBackQueue()
	var executed bool
	err := q.Push(func(_ context.Context, _ time.Duration) {
		executed = true
	})
	assertErrorIs(t, err, ErrQueueClosed, "Push should return an error after queue close")
	assertTrue(t, !executed, "Callback should not be executed after queue close")
}

func TestCallbackQueue_PushNilPanics(t *testing.T) {
	q := OpenCallBackQueue()
	defer q.Close()
	defer func() {
		if v := recover(); v == nil {
			t.Fatalf("expected panic when pushing nil callback, got none")
		}
	}()
	_ = q.Push(nil)
}
