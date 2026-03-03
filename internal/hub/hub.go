// Package hub provides a shared SSE publish-subscribe hub used by both the
// watcher (agent) and warden servers.
// Pattern: publish-subscribe hub (fan-out broadcaster).
package hub

import "sync"

// ClientInfo stores information about a connected SSE client.
type ClientInfo struct {
	IP      string
	Channel chan []byte
}

// Hub manages SSE client subscriptions and broadcasts events to all clients.
type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]*ClientInfo
	limiter *ConnectionLimiter
}

// NewHub creates an empty Hub with default connection limits.
func NewHub() *Hub {
	return NewHubWithLimits(DefaultMaxPerIP, DefaultMaxTotal)
}

// NewHubWithLimits creates a Hub with specified connection limits.
func NewHubWithLimits(maxPerIP, maxTotal int) *Hub {
	return &Hub{
		clients: make(map[chan []byte]*ClientInfo),
		limiter: NewConnectionLimiter(maxPerIP, maxTotal),
	}
}

// Subscribe creates and registers a new event channel for a client.
// Returns error if connection limits are exceeded.
func (h *Hub) Subscribe(clientIP string) (chan []byte, error) {
	// Check limits before creating resources
	if !h.limiter.CanConnect(clientIP) {
		return nil, ErrTooManyConnections
	}

	// Try to add connection under lock
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.limiter.AddConnection(clientIP) {
		return nil, ErrTooManyConnections
	}

	ch := make(chan []byte, DefaultBufferSize)
	h.clients[ch] = &ClientInfo{
		IP:      clientIP,
		Channel: ch,
	}

	return ch, nil
}

// Unsubscribe removes the channel from the hub and closes it.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if info, ok := h.clients[ch]; ok {
		h.limiter.RemoveConnection(info.IP)
		delete(h.clients, ch)
		close(ch)
	}
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

// Stats returns current SSE connection statistics.
func (h *Hub) Stats() (totalConnections int, uniqueIPs int, perIPConnections map[string]int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.limiter != nil {
		total, unique := h.limiter.Stats()
		return total, unique, h.limiter.ConnectionsPerIP()
	}
	return len(h.clients), 0, nil
}

// ConnectionCount returns the current number of active SSE connections.
func (h *Hub) ConnectionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
