package managementmode

import "sync"

// Reader exposes the current management mode state.
type Reader interface {
	Current() State
}

// Store holds the current management mode state for readers across controllers.
type Store struct {
	mu    sync.RWMutex
	state State
}

// NewStore creates a Store with the given initial state snapshot.
func NewStore(initial State) *Store {
	return &Store{state: initial}
}

// Update replaces the current state snapshot.
func (s *Store) Update(state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}

// Current returns a copy of the current state snapshot.
func (s *Store) Current() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}
