package queues

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/centrifuge-go/internal/lists"
)

// ErrQueueClosed is returned when the queue is closed and an attempt is made to
// interact with the queue.
var ErrQueueClosed = errors.New("queue is closed")

// CallBackQueue runs callbacks in the order they are pushed to the queue. It is
// used as a synchronization mechanism for invoking functions in order across
// goroutines. It should not be used after being closed.
type CallBackQueue struct {
	// The ordered list of callbacks to be processed.
	list *lists.List[*callBackRequest]
	// enqueueSignals signals when a new item is added. it must be a buffered
	// channel to avoid missing signals.
	enqueueSignals chan struct{}
	// running prevents concurrent processCallBacks execution.
	running sync.Mutex
	// If true, the queue must not be used; return ErrQueueClosed.
	opened atomic.Bool
	// closeSignal is closed as a signal to shut down the queue processing.
	closeSignal chan struct{}
}

// newUnopenedCallBackQueue creates a queue in the closed state. Use
// OpenCallBackQueue instead.
func newUnopenedCallBackQueue() *CallBackQueue {
	return &CallBackQueue{
		list:           lists.NewList[*callBackRequest](),
		enqueueSignals: make(chan struct{}, 1),
		closeSignal:    make(chan struct{}),
	}
}

// OpenCallBackQueue creates a new callback queue and starts the queuing
// process. The caller is responsible for closing the queue when it is no longer
// needed. The queue cannot be reused after it is closed.
func OpenCallBackQueue() *CallBackQueue {
	q := newUnopenedCallBackQueue()
	q.running.Lock()
	processCallBacksIsRunning := make(chan struct{})
	go func() {
		defer q.running.Unlock()
		defer q.Close()
		defer func() {
			if v := recover(); v != nil {
				// This should not happen, but if it does, this defer only adds
				// a description to the panic. It does not stop it from exiting
				// the program.
				panic(fmt.Sprintf("callback queue panicked: %v", v))
			}
		}()
		q.opened.Store(true)
		close(processCallBacksIsRunning)
		q.processCallBacks()
	}()
	// wait for the goroutine to start processing callbacks before allowing
	// interactions with the queue.
	<-processCallBacksIsRunning
	return q
}

// Close cleans up the resources of the queue. It will block until the queue is
// fully closed, canceling any remaining callbacks from being processed. The
// queue cannot be reused after closing. Calling Close multiple times is a
// no-op. The caller must call Close when done with the queue.
func (q *CallBackQueue) Close() {
	if !q.opened.Swap(false) {
		return // The queue is already closed.
	}
	close(q.closeSignal)
	// Obtain the running lock to ensure the queue is finished processing before
	// returning.
	q.running.Lock()
	defer q.running.Unlock()
	q.list.Clear()
}

// processCallBacks is responsible for invoking callbacks from the list when it
// is signaled to do so. It blocks forever until the queue is closed.
func (q *CallBackQueue) processCallBacks() {
	for q.nextCallBack() {
		q.invokeOneCallBack()
	}
}

func (q *CallBackQueue) nextCallBack() bool {
	if q.list.Len() > 0 {
		return true
	}
	select {
	case <-q.closeSignal:
		return false
	case <-q.enqueueSignals:
		return true
	}
}

// signalEnqueue wakes nextCallBack.
func (q *CallBackQueue) signalEnqueue() {
	select {
	case q.enqueueSignals <- struct{}{}:
	default:
	}
}

// invokeOneCallBack is responsible for invoking a single callback from the
// list.
func (q *CallBackQueue) invokeOneCallBack() {
	curr, ok := q.list.PopFront()
	if !ok {
		return
	}
	if curr == nil {
		return
	}
	if curr.fn == nil {
		return
	}
	// signal to cancel the callback if the queue is closed while the callback
	// is executing.
	callbackCtx, callbackCancel := context.WithCancel(context.Background())
	defer callbackCancel()
	go func() {
		select {
		case <-q.closeSignal:
			callbackCancel()
		case <-callbackCtx.Done():
			return
		}
	}()
	curr.fn(callbackCtx, time.Since(curr.tm))
}

// Push adds a callback to the queue. It blocks until the callback is added to
// the queue. It panics if cb is nil. It returns ErrQueueClosed if the queue is
// closed. The duration passed to cb is the time since the callback was
// enqueued.
func (q *CallBackQueue) Push(cb CallBackFunc) error {
	if cb == nil {
		panic("nil callback function")
	}
	if !q.opened.Load() {
		return ErrQueueClosed
	}
	// Preserve order.
	q.list.PushBack(&callBackRequest{fn: cb, tm: time.Now()})
	q.signalEnqueue()
	return nil
}

// CallBackFunc is a function type that represents a callback to be executed.
// The ctx is canceled if the queue is closed while the callback is executing.
// The delay is the time since the callback was added to the queue.
type CallBackFunc = func(ctx context.Context, delay time.Duration)

// callBackRequest represents a callback and the time it was added to the queue.
type callBackRequest struct {
	fn CallBackFunc
	// The time the callback was added to the queue.
	tm time.Time
}
