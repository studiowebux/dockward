// Package logger provides structured logging with journald/syslog support.
package logger

import (
	"fmt"
	"log"
	"log/syslog"
	"os"
	"sync"
)

var (
	syslogWriter *syslog.Writer
	mu           sync.Mutex
	initialized  bool
)

// Init initializes the logger with syslog support.
// Falls back to stderr if syslog is unavailable.
func Init(tag string) {
	mu.Lock()
	defer mu.Unlock()

	if initialized {
		return
	}

	var err error
	syslogWriter, err = syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, tag)
	if err != nil {
		// Fallback to stderr
		fmt.Fprintf(os.Stderr, "Failed to connect to syslog: %v\n", err)
		fmt.Fprintf(os.Stderr, "Falling back to stderr logging\n")
	}
	initialized = true
}

// Close closes the syslog connection.
func Close() {
	mu.Lock()
	defer mu.Unlock()

	if syslogWriter != nil {
		_ = syslogWriter.Close()
		syslogWriter = nil
	}
	initialized = false
}

// Info logs an informational message.
func Info(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)

	mu.Lock()
	defer mu.Unlock()

	if syslogWriter != nil {
		_ = syslogWriter.Info(msg)
	} else {
		log.Printf("[INFO] %s", msg)
	}
}

// Warning logs a warning message.
func Warning(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)

	mu.Lock()
	defer mu.Unlock()

	if syslogWriter != nil {
		_ = syslogWriter.Warning(msg)
	} else {
		log.Printf("[WARNING] %s", msg)
	}
}

// Error logs an error message.
func Error(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)

	mu.Lock()
	defer mu.Unlock()

	if syslogWriter != nil {
		_ = syslogWriter.Err(msg)
	} else {
		log.Printf("[ERROR] %s", msg)
	}
}

// Critical logs a critical error message.
func Critical(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)

	mu.Lock()
	defer mu.Unlock()

	if syslogWriter != nil {
		_ = syslogWriter.Crit(msg)
	} else {
		log.Printf("[CRITICAL] %s", msg)
	}
}

// Debug logs a debug message (only to stderr, not syslog).
func Debug(format string, args ...interface{}) {
	if os.Getenv("DOCKWARD_DEBUG") != "" {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// Printf is a drop-in replacement for log.Printf that uses syslog.
func Printf(format string, args ...interface{}) {
	Info(format, args...)
}

// Fatalf is a drop-in replacement for log.Fatalf that uses syslog.
func Fatalf(format string, args ...interface{}) {
	Critical(format, args...)
	os.Exit(1)
}