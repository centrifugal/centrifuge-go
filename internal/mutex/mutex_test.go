package mutex

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestMutexWaitLock(t *testing.T) {
	var mu Mutex
	mu.WaitLock() <- struct{}{}
	defer mu.Unlock()
	if len(mu.state) != 1 {
		t.Fatal("failed to set lock state")
	}
}

func TestMutexLock(t *testing.T) {
	var mu Mutex
	mu.Lock()
	defer mu.Unlock()
	if len(mu.state) != 1 {
		t.Fatal("failed to set lock state")
	}
}

func TestMutexTryLock(t *testing.T) {
	var mu Mutex
	if !mu.TryLock() {
		t.Fatal("failed to obtain lock")
	}
	defer mu.Unlock()
	if len(mu.state) != 1 {
		t.Fatal("failed to set lock state")
	}
}

func TestMutexTryLock_already_locked(t *testing.T) {
	var mu Mutex
	mu.init()
	mu.state <- struct{}{}
	if mu.TryLock() {
		t.Fatal("obtain lock")
	}
	defer mu.Unlock()
	if len(mu.state) != 1 {
		t.Fatal("failed to set lock state")
	}
}

func TestMutexLockCtx(t *testing.T) {
	var mu Mutex
	if err := mu.LockCtx(t.Context()); err != nil {
		t.Fatal("failed to obtain lock")
	}
	defer mu.Unlock()
	if len(mu.state) != 1 {
		t.Fatal("failed to set lock state")
	}
}

func TestMutexLockCtx_cancels(t *testing.T) {
	var mu Mutex
	mu.init()
	mu.state <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	go cancel()
	if err := mu.LockCtx(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("did not receive context cancel error")
	}
}

func TestMutexUnlock(t *testing.T) {
	var mu Mutex
	mu.init()
	mu.state <- struct{}{}
	mu.Unlock()
	if len(mu.state) != 0 {
		t.Fatal("failed to set unlock state")
	}
}

func TestMutexUnlock_panics_when_unlocked(t *testing.T) {
	var mu Mutex
	defer func() {
		if v := recover(); v == nil {
			t.Fatal("failed to panic when unlocking an unlocked mutex")
		}
		if len(mu.state) != 0 {
			t.Fatal("mutated state of unlocked mutex")
		}
	}()
	mu.Unlock()
}

// if Mutex does not work this is a race condition.
// must be tested with "-race"
func TestMutexLock_race(t *testing.T) {
	var mu Mutex
	var x int
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			x++
		}()
	}
	wg.Wait()
}
