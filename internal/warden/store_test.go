package warden

import (
	"fmt"
	"testing"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
)

func makeEntry(msg string) audit.Entry {
	return audit.Entry{
		Timestamp: time.Now().UTC(),
		Service:   "svc",
		Event:     "test",
		Message:   msg,
		Level:     "info",
	}
}

func TestStore_Recent_ReturnsNewestFirst(t *testing.T) {
	s := NewStore(nil)
	s.Append(makeEntry("first"))
	s.Append(makeEntry("second"))
	s.Append(makeEntry("third"))

	got := s.Recent(10)

	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	if got[0].Message != "third" {
		t.Errorf("want newest first: got %q", got[0].Message)
	}
	if got[2].Message != "first" {
		t.Errorf("want oldest last: got %q", got[2].Message)
	}
}

func TestStore_Recent_FewerThanRequested(t *testing.T) {
	s := NewStore(nil)
	s.Append(makeEntry("only"))

	got := s.Recent(50)

	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
}

func TestStore_Recent_Empty(t *testing.T) {
	s := NewStore(nil)

	got := s.Recent(10)

	if len(got) != 0 {
		t.Fatalf("want empty slice, got %d entries", len(got))
	}
}

func TestStore_Recent_ZeroN(t *testing.T) {
	s := NewStore(nil)
	s.Append(makeEntry("msg"))

	got := s.Recent(0)

	if len(got) != 0 {
		t.Fatalf("want empty slice for n=0, got %d entries", len(got))
	}
}

func TestStore_RingBuffer_WrapAround(t *testing.T) {
	s := NewStore(nil)

	// Write one more than capacity to trigger wrap-around.
	for i := 0; i <= ringSize; i++ {
		s.Append(makeEntry(fmt.Sprintf("msg-%d", i)))
	}

	got := s.Recent(ringSize)

	if len(got) != ringSize {
		t.Fatalf("want %d entries after wrap, got %d", ringSize, len(got))
	}
	// Newest entry is msg-ringSize (the last one written).
	if got[0].Message != fmt.Sprintf("msg-%d", ringSize) {
		t.Errorf("want newest entry msg-%d, got %q", ringSize, got[0].Message)
	}
	// msg-0 should have been overwritten; oldest surviving is msg-1.
	if got[ringSize-1].Message != "msg-1" {
		t.Errorf("want oldest surviving to be msg-1, got %q", got[ringSize-1].Message)
	}
}

func TestStore_AgentState_StartsOffline(t *testing.T) {
	s := NewStore([]AgentConfig{{ID: "a1", URL: "http://a1", Token: "tok"}})

	states := s.AgentStates()

	if len(states) != 1 {
		t.Fatalf("want 1 agent state, got %d", len(states))
	}
	if states[0].Online {
		t.Error("new agent should start offline")
	}
}

func TestStore_SetAgentState_Online(t *testing.T) {
	s := NewStore([]AgentConfig{{ID: "a1", URL: "http://a1", Token: "tok"}})

	s.SetAgentState("a1", true)
	states := s.AgentStates()

	if !states[0].Online {
		t.Error("agent should be online after SetAgentState(true)")
	}
	if states[0].LastSeen.IsZero() {
		t.Error("LastSeen should be set after SetAgentState")
	}
}

func TestStore_SetAgentState_UnknownIDIsNoop(t *testing.T) {
	s := NewStore(nil)

	// Should not panic.
	s.SetAgentState("nonexistent", true)
}

func TestStore_SaveAndLoadState(t *testing.T) {
	path := t.TempDir() + "/state.json"

	// Populate a store and save.
	src := NewStore(nil)
	src.Append(makeEntry("alpha"))
	src.Append(makeEntry("beta"))
	src.SaveState(path)

	// Load into a fresh store and verify.
	dst := NewStore(nil)
	dst.LoadState(path)

	got := dst.Recent(10)
	if len(got) != 2 {
		t.Fatalf("want 2 entries after load, got %d", len(got))
	}
	// Recent returns newest first; beta was appended last.
	if got[0].Message != "beta" {
		t.Errorf("want newest first after load: got %q", got[0].Message)
	}
}

func TestStore_LoadState_MissingFileIsNoop(t *testing.T) {
	s := NewStore(nil)

	// Should not panic or error.
	s.LoadState("/tmp/dockward-nonexistent-state-file.json")

	if len(s.Recent(1)) != 0 {
		t.Error("want empty store after loading missing file")
	}
}

func TestStore_LoadState_EmptyPathIsNoop(t *testing.T) {
	s := NewStore(nil)
	s.LoadState("") // should be a no-op
}
