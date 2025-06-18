package lists

import (
	"container/list"
	"sync"
)

// List is a generic list that is safe for concurrent use. It is mostly a
// convenience wrapper around the 'container/list' package.
type List[T any] struct {
	mu     sync.Mutex
	values *list.List
}

func NewList[T any]() *List[T] {
	return &List[T]{
		values: list.New(),
	}
}

// PushBack adds a new element to the back of the list.
func (l *List[T]) PushBack(value T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values.PushBack(value)
}

// PopFront removes and returns the first element of the list. It returns false
// if the list is empty.
func (l *List[T]) PopFront() (T, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.values.Len() == 0 {
		var zero T
		return zero, false
	}
	elem := l.values.Front()
	l.values.Remove(elem)
	return elem.Value.(T), true
}

// Len returns the number of elements in the list.
func (l *List[T]) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.values.Len()
}

// Clear removes all elements from the list, making it empty.
func (l *List[T]) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values.Init()
}
