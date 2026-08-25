package store

import "sync"

type Store struct {
	mu    sync.Mutex
	value int
}

func (s *Store) Get() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

func (s *Store) Update(change func(current int) int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = change(s.value)
}
