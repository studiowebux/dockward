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
	"path/filepath"
	"sort"
	"strings"
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
	mu        sync.Mutex
	file      *os.File     // nil when disabled
	push      Pusher       // nil when push is disabled
	bcast     Broadcaster  // nil when broadcast is disabled
	path      string       // path to log file
	maxSizeMB int          // max size in MB before rotation (default: 100MB)
	maxEvents int          // max events to keep in current file (default: 10000)
	eventCount int         // current event count
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

	// Count existing events in the file
	eventCount := 0
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		// Reopen for reading to count lines
		rf, err := os.Open(path)
		if err == nil {
			scanner := bufio.NewScanner(rf)
			for scanner.Scan() {
				eventCount++
			}
			rf.Close()
		}
	}

	return &Logger{
		file:       f,
		path:       path,
		maxSizeMB:  100,   // Default 100MB
		maxEvents:  10000, // Default 10k events
		eventCount: eventCount,
	}, nil
}

// SetLimits configures rotation limits. Call after New().
func (l *Logger) SetLimits(maxSizeMB, maxEvents int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if maxSizeMB > 0 {
		l.maxSizeMB = maxSizeMB
	}
	if maxEvents > 0 {
		l.maxEvents = maxEvents
	}
}

// rotate moves the current log to a timestamped backup and starts fresh
func (l *Logger) rotate() error {
	if l.file == nil {
		return nil
	}

	// Close current file
	l.file.Close()

	// Generate archive name with timestamp
	now := time.Now()
	archivePath := fmt.Sprintf("%s.%s.jsonl",
		strings.TrimSuffix(l.path, ".jsonl"),
		now.Format("20060102-150405"))

	// Rename current to archive
	if err := os.Rename(l.path, archivePath); err != nil {
		// If rename fails, try to reopen original
		f, _ := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		l.file = f
		return fmt.Errorf("rotate rename failed: %w", err)
	}

	// Open new file
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("rotate open new: %w", err)
	}

	l.file = f
	l.eventCount = 0
	log.Printf("[audit] rotated log to %s", archivePath)

	// Clean up old archives (keep last 5)
	l.cleanOldArchives()

	return nil
}

// cleanOldArchives removes old archive files, keeping only the most recent 5
func (l *Logger) cleanOldArchives() {
	dir := filepath.Dir(l.path)
	base := filepath.Base(strings.TrimSuffix(l.path, ".jsonl"))
	pattern := base + ".*.jsonl"

	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) <= 5 {
		return
	}

	// Sort by name (timestamp makes them sortable)
	sort.Strings(matches)

	// Remove oldest ones
	for i := 0; i < len(matches)-5; i++ {
		if err := os.Remove(matches[i]); err == nil {
			log.Printf("[audit] removed old archive: %s", matches[i])
		}
	}
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

	// Check if rotation needed before write
	if l.shouldRotate() {
		if err := l.rotate(); err != nil {
			log.Printf("[audit] rotation failed: %v", err)
		}
	}

	_, err = fmt.Fprintf(l.file, "%s\n", data)
	if err == nil {
		l.eventCount++
	}
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

// shouldRotate checks if log should be rotated based on size or event count
func (l *Logger) shouldRotate() bool {
	if l.file == nil {
		return false
	}

	// Check event count
	if l.eventCount >= l.maxEvents {
		return true
	}

	// Check file size
	if info, err := l.file.Stat(); err == nil {
		sizeMB := info.Size() / (1024 * 1024)
		if sizeMB >= int64(l.maxSizeMB) {
			return true
		}
	}

	return false
}
