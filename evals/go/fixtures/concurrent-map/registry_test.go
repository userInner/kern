package registry

import (
	"fmt"
	"sync"
	"testing"
)

func TestRegistryConcurrentAccess(t *testing.T) {
	registry := New()
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for index := 0; index < 1_000; index++ {
				key := fmt.Sprintf("key-%d", index%16)
				registry.Set(key, worker)
				_, _ = registry.Get(key)
			}
		}(worker)
	}
	workers.Wait()
}
