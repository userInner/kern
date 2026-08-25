package store

import (
	"testing"
	"time"
)

func TestUpdateCallbackCanReadStore(t *testing.T) {
	store := &Store{value: 2}
	done := make(chan struct{})
	go func() {
		store.Update(func(current int) int {
			return current + store.Get()
		})
		close(done)
	}()
	select {
	case <-done:
		if got := store.Get(); got != 4 {
			t.Fatalf("Get() = %d, want 4", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Update deadlocked while invoking callback")
	}
}
