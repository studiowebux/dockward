package warden

import (
	"encoding/json"
	"github.com/studiowebux/dockward/internal/logger"
	"os"
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

// LoadState reads a previously saved ring buffer from path and appends the
// entries into the store. No-op when path is empty or the file does not exist.
func (s *Store) LoadState(path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path from config, not user input
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Printf("warden: load state %s: %v", path, err)
		}
		return
	}
	var entries []audit.Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		logger.Printf("warden: parse state %s: %v", path, err)
		return
	}
	for _, e := range entries {
		s.Append(e)
	}
	logger.Printf("warden: restored %d event(s) from %s", len(entries), path)
}

// SaveState writes the current ring buffer (newest-first) to path as JSON.
// No-op when path is empty.
func (s *Store) SaveState(path string) {
	if path == "" {
		return
	}
	events := s.Recent(ringSize) // newest first; reverse for chronological order
	// Reverse to oldest-first so LoadState replays in the right order.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	data, err := json.Marshal(events)
	if err != nil {
		logger.Printf("warden: marshal state: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil { // #nosec G306
		logger.Printf("warden: save state %s: %v", path, err)
		return
	}
	logger.Printf("warden: saved %d event(s) to %s", len(events), path)
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
