package warden

import (
	"sync"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
)

const ringSize = 200

// AgentState tracks connectivity for one agent.
type AgentState struct {
	ID       string
	URL      string
	LastSeen time.Time
	Online   bool
}

// Store holds the event ring buffer and per-agent connectivity state.
// Pattern: ring buffer (fixed-size array with wrap-around write pointer).
type Store struct {
	mu     sync.RWMutex
	events [ringSize]audit.Entry
	head   int // next write index
	count  int // total entries stored (max ringSize)
	agents map[string]*AgentState
}

// NewStore creates a Store pre-populated with AgentState entries from cfg.
func NewStore(agents []AgentConfig) *Store {
	s := &Store{
		agents: make(map[string]*AgentState, len(agents)),
	}
	for _, a := range agents {
		s.agents[a.ID] = &AgentState{
			ID:  a.ID,
			URL: a.URL,
		}
	}
	return s
}

// Append adds an entry to the ring buffer, overwriting the oldest entry when full.
func (s *Store) Append(e audit.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events[s.head] = e
	s.head = (s.head + 1) % ringSize
	if s.count < ringSize {
		s.count++
	}
}

// Recent returns the last n entries, newest first.
// If fewer than n entries exist, all entries are returned.
func (s *Store) Recent(n int) []audit.Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if n <= 0 || s.count == 0 {
		return nil
	}
	if n > s.count {
		n = s.count
	}

	out := make([]audit.Entry, n)
	for i := 0; i < n; i++ {
		// Walk backwards from the most recently written slot.
		idx := (s.head - 1 - i + ringSize) % ringSize
		out[i] = s.events[idx]
	}
	return out
}

// SetAgentState updates connectivity status for the given agent ID.
// LastSeen is always set to now; Online is set to the provided value.
func (s *Store) SetAgentState(id string, online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if a, ok := s.agents[id]; ok {
		a.Online = online
		a.LastSeen = time.Now().UTC()
	}
}

// AgentStates returns a snapshot of all agent states.
func (s *Store) AgentStates() []AgentState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]AgentState, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, *a)
	}
	return out
}
