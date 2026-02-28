package warden

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
)

const heartbeatInterval = 30 * time.Second

// Heartbeat polls each configured agent's GET /health endpoint every 30 seconds
// and emits synthetic audit entries on state transitions (online → offline, offline → online).
type Heartbeat struct {
	store  *Store
	hub    *Hub
	agents []AgentConfig
	http   *http.Client
	// online tracks the last known online state per agent ID.
	online map[string]bool
}

// NewHeartbeat creates a Heartbeat ready to run.
func NewHeartbeat(store *Store, hub *Hub, agents []AgentConfig) *Heartbeat {
	online := make(map[string]bool, len(agents))
	for _, a := range agents {
		online[a.ID] = false // assume offline until first successful check
	}
	return &Heartbeat{
		store:  store,
		hub:    hub,
		agents: agents,
		http:   &http.Client{Timeout: 5 * time.Second},
		online: online,
	}
}

// Run polls agents on each heartbeat tick until ctx is cancelled.
func (hb *Heartbeat) Run(ctx context.Context) {
	// Poll immediately on start, then on each tick.
	hb.pollAll(ctx)

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hb.pollAll(ctx)
		}
	}
}

func (hb *Heartbeat) pollAll(ctx context.Context) {
	for _, a := range hb.agents {
		hb.poll(ctx, a)
	}
}

func (hb *Heartbeat) poll(ctx context.Context, a AgentConfig) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL+"/health", nil)
	if err != nil {
		hb.transition(a.ID, false)
		return
	}

	resp, err := hb.http.Do(req)
	if err != nil {
		hb.transition(a.ID, false)
		return
	}
	resp.Body.Close()

	hb.transition(a.ID, resp.StatusCode == http.StatusOK)
}

// transition handles an online/offline state change for an agent.
// Emits a synthetic audit entry only when the state changes.
func (hb *Heartbeat) transition(id string, nowOnline bool) {
	wasOnline := hb.online[id]
	hb.online[id] = nowOnline

	hb.store.SetAgentState(id, nowOnline)

	if wasOnline == nowOnline {
		return // no change; nothing to emit
	}

	event := "agent_online"
	level := "info"
	msg := "Agent came online."
	if !nowOnline {
		event = "agent_offline"
		level = "warning"
		msg = "Agent went offline."
	}

	entry := audit.Entry{
		Timestamp: time.Now().UTC(),
		Machine:   id,
		Service:   "warden",
		Event:     event,
		Message:   msg,
		Level:     level,
	}

	hb.store.Append(entry)

	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("warden: heartbeat marshal: %v", err)
		return
	}
	hb.hub.Broadcast(data)
}
