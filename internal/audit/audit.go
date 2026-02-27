// Package audit writes structured JSON audit log entries to a file.
// One JSON object per line (JSON Lines format). Concurrent writes are
// serialised with a mutex so updater and healer goroutines can both write.
// Disabled (no-op) when path is empty.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
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

// Logger appends Entry values to a JSON Lines file.
// A nil or zero-value Logger is safe to use — all operations are no-ops.
type Logger struct {
	mu   sync.Mutex
	file *os.File // nil when disabled
}

// New opens (or creates) the audit log file at path.
// Returns a no-op Logger when path is empty.
func New(path string) (*Logger, error) {
	if path == "" {
		return &Logger{}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) // #nosec G304 -- path from config, not user input
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
	defer l.mu.Unlock()
	_, err = fmt.Fprintf(l.file, "%s\n", data)
	return err
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

	f, err := os.Open(name) // #nosec G304 -- opening our own log file
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
