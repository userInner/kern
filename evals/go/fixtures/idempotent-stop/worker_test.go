package worker

import (
	"sync"
	"testing"
)

func TestStopIsIdempotent(t *testing.T) {
	worker := New()
	worker.Stop()
	worker.Stop()
	select {
	case <-worker.Done():
	default:
		t.Fatal("Done channel is still open")
	}
}

func TestConcurrentStop(t *testing.T) {
	worker := New()
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			worker.Stop()
		}()
	}
	group.Wait()
}
