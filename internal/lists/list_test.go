package lists

import (
	"testing"
)

func TestList_PushBack(t *testing.T) {
	l := NewList[int]()
	if l.Len() != 0 {
		t.Fatalf("expected length 0, got %d", l.Len())
	}
	l.PushBack(1)
	l.PushBack(2)
	l.PushBack(3)
	if l.Len() != 3 {
		t.Fatalf("expected length 3, got %d", l.Len())
	}
}

func TestList_PopFront(t *testing.T) {
	l := NewList[string]()
	l.PushBack("a")
	l.PushBack("b")
	l.PushBack("c")
	v, ok := l.PopFront()
	if !ok || v != "a" {
		t.Fatalf("expected 'a', got '%v', ok=%v", v, ok)
	}
	v, ok = l.PopFront()
	if !ok || v != "b" {
		t.Fatalf("expected 'b', got '%v', ok=%v", v, ok)
	}
	v, ok = l.PopFront()
	if !ok || v != "c" {
		t.Fatalf("expected 'c', got '%v', ok=%v", v, ok)
	}
	_, ok = l.PopFront()
	if ok {
		t.Fatalf("expected false on empty list")
	}
}
