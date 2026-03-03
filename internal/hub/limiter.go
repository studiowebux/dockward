package hub

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Default connection limits
const (
	DefaultMaxPerIP    = 10
	DefaultMaxTotal    = 1000
	DefaultBufferSize  = 64
)

// ErrTooManyConnections is returned when connection limits are exceeded.
var ErrTooManyConnections = errors.New("too many connections")

// ConnectionLimiter tracks and limits SSE connections.
type ConnectionLimiter struct {
	mu               sync.RWMutex
	perIPConnections map[string]int
	totalConnections int
	maxPerIP         int
	maxTotal         int
}

// NewConnectionLimiter creates a limiter with the specified limits.
func NewConnectionLimiter(maxPerIP, maxTotal int) *ConnectionLimiter {
	if maxPerIP <= 0 {
		maxPerIP = DefaultMaxPerIP
	}
	if maxTotal <= 0 {
		maxTotal = DefaultMaxTotal
	}
	return &ConnectionLimiter{
		perIPConnections: make(map[string]int),
		maxPerIP:         maxPerIP,
		maxTotal:         maxTotal,
	}
}

// CanConnect checks if a new connection from the given IP is allowed.
func (l *ConnectionLimiter) CanConnect(clientIP string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Check total limit
	if l.totalConnections >= l.maxTotal {
		return false
	}

	// Check per-IP limit
	if l.perIPConnections[clientIP] >= l.maxPerIP {
		return false
	}

	return true
}

// AddConnection registers a new connection from the given IP.
// Returns false if limits would be exceeded.
func (l *ConnectionLimiter) AddConnection(clientIP string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Re-check limits under write lock
	if l.totalConnections >= l.maxTotal {
		return false
	}
	if l.perIPConnections[clientIP] >= l.maxPerIP {
		return false
	}

	l.perIPConnections[clientIP]++
	l.totalConnections++
	return true
}

// RemoveConnection decrements the connection count for the given IP.
func (l *ConnectionLimiter) RemoveConnection(clientIP string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if count := l.perIPConnections[clientIP]; count > 0 {
		if count == 1 {
			delete(l.perIPConnections, clientIP)
		} else {
			l.perIPConnections[clientIP]--
		}
		l.totalConnections--
	}
}

// Stats returns current connection statistics.
func (l *ConnectionLimiter) Stats() (totalConnections int, uniqueIPs int) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.totalConnections, len(l.perIPConnections)
}

// ConnectionsPerIP returns a copy of the current per-IP connection counts.
func (l *ConnectionLimiter) ConnectionsPerIP() map[string]int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make(map[string]int, len(l.perIPConnections))
	for ip, count := range l.perIPConnections {
		result[ip] = count
	}
	return result
}

// ExtractClientIP extracts the client IP address from an HTTP request.
// It handles X-Forwarded-For and X-Real-IP headers for proxy scenarios.
func ExtractClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (common for proxies)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the chain
		if idx := strings.Index(xff, ","); idx != -1 {
			xff = xff[:idx]
		}
		xff = strings.TrimSpace(xff)
		if ip := net.ParseIP(xff); ip != nil {
			return ip.String()
		}
	}

	// Check X-Real-IP header (nginx)
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr might not have a port
		host = r.RemoteAddr
	}

	// Parse to normalize the IP
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}

	// Last resort: return as-is
	return host
}