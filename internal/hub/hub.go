// Package hub provides a shared SSE publish-subscribe hub used by both the
// watcher (agent) and warden servers.
// Pattern: publish-subscribe hub (fan-out broadcaster).
package hub

import "sync"

// Hub manages SSE client subscriptions and broadcasts events to all clients.
type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

// NewHub creates an empty Hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[chan []byte]struct{}),
	}
}

// Subscribe creates and registers a new event channel for a client.
func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes the channel from the hub and closes it.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// Broadcast sends data to all subscribed clients.
// Drops the message for a client whose channel is full (non-blocking).
func (h *Hub) Broadcast(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.clients {
		select {
		case ch <- data:
		default:
			// Slow client: drop message rather than block.
		}
	}
}
