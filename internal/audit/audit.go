// Package audit writes structured JSON audit log entries to a file.
// One JSON object per line (JSON Lines format). Concurrent writes are
// serialised with a mutex so updater and healer goroutines can both write.
// Disabled (no-op) when path is empty.
package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// Entry is a single audit log record.
type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Machine   string    `json:"machine,omitempty"`   // reserved for warden (Goal #7)
	Service   string    `json:"service"`
	Event     string    `json:"event"`
	Message   string    `json:"message"`
	Level     string    `json:"level"`
	OldDigest string    `json:"old_digest,omitempty"`
	NewDigest string    `json:"new_digest,omitempty"`
	Container string    `json:"container,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// Pusher forwards audit entries to a remote warden.
// Implemented by push.Client. Defined here to avoid an import cycle.
type Pusher interface {
	Send(ctx context.Context, e Entry) error
}

// Broadcaster receives new audit entries for local fan-out (e.g. SSE hub).
// Implemented by watcher broadcaster adapter. Defined here to avoid an import cycle.
type Broadcaster interface {
	Broadcast(e Entry)
}

// Logger appends Entry values to a JSON Lines file.
// A nil or zero-value Logger is safe to use — all operations are no-ops.
type Logger struct {
	mu    sync.Mutex
	file  *os.File     // nil when disabled
	push  Pusher       // nil when push is disabled
	bcast Broadcaster  // nil when broadcast is disabled
}

// WithPush attaches a Pusher to the logger. Returns the same logger (fluent).
// Safe to call on a nil logger (returns nil).
func (l *Logger) WithPush(p Pusher) *Logger {
	if l == nil {
		return nil
	}
	l.push = p
	return l
}

// WithBroadcast attaches a Broadcaster to the logger. Returns the same logger (fluent).
// Safe to call on a nil logger (returns nil).
func (l *Logger) WithBroadcast(b Broadcaster) *Logger {
	if l == nil {
		return nil
	}
	l.bcast = b
	return l
}

// New opens (or creates) the audit log file at path.
// Returns a no-op Logger when path is empty.
func New(path string) (*Logger, error) {
	if path == "" {
		return &Logger{}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600) // #nosec G304 -- path from config, not user input
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}
	return &Logger{file: f}, nil
}

// Write appends a JSON-encoded Entry followed by a newline.
// Timestamps the entry if Timestamp is zero. No-op when the logger is disabled.
func (l *Logger) Write(e Entry) error {
	if l == nil || l.file == nil {
		return nil
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal audit entry: %w", err)
	}
	l.mu.Lock()
	_, err = fmt.Fprintf(l.file, "%s\n", data)
	p := l.push
	b := l.bcast
	l.mu.Unlock()

	if err != nil {
		return err
	}

	// Fire-and-forget push to warden.
	if p != nil {
		go func() {
			if sendErr := p.Send(context.Background(), e); sendErr != nil {
				log.Printf("[audit] push to warden failed: %v", sendErr)
			}
		}()
	}

	// Fan out to local SSE hub.
	if b != nil {
		go b.Broadcast(e)
	}

	return nil
}

// Recent reads the last n entries from the log file.
// Returns an empty slice when the logger is disabled or the file is empty.
// Reads the whole file; acceptable for typical log sizes (<10k lines).
func (l *Logger) Recent(n int) ([]Entry, error) {
	if l == nil || l.file == nil || n <= 0 {
		return nil, nil
	}

	l.mu.Lock()
	name := l.file.Name()
	l.mu.Unlock()

	f, err := os.Open(name) // #nosec G304,G703 -- opening our own log file, path from config
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	defer f.Close()

	// Collect all lines, keep last n.
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan audit log: %w", err)
	}

	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	entries := make([]Entry, 0, len(lines))
	for _, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// Close flushes and closes the underlying file.
// No-op when the logger is disabled.
func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
