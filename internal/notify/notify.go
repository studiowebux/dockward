// Package notify dispatches alert messages to configured notification channels.
package notify

import (
	"context"
	"log"
	"time"
)

// Event severity levels for notification color/priority.
const (
	LevelInfo     = "info"
	LevelWarning  = "warning"
	LevelCritical = "critical"
)

// Alert is the data passed to all notifiers.
type Alert struct {
	Service   string
	Event     string // updated, rolled_back, unhealthy, restarted, critical, healthy, died
	Message   string
	Reason    string
	OldDigest string
	NewDigest string
	Container string
	Timestamp time.Time
	Level     string // info, warning, critical
}

// Notifier sends an alert through a specific channel.
type Notifier interface {
	Name() string
	Send(ctx context.Context, alert Alert) error
}

// Dispatcher fans out alerts to all registered notifiers.
type Dispatcher struct {
	notifiers []Notifier
}

// NewDispatcher creates a dispatcher with the given notifiers.
func NewDispatcher(notifiers ...Notifier) *Dispatcher {
	return &Dispatcher{notifiers: notifiers}
}

// Send dispatches an alert to all notifiers. Logs errors but does not fail.
func (d *Dispatcher) Send(ctx context.Context, alert Alert) {
	if alert.Timestamp.IsZero() {
		alert.Timestamp = time.Now().UTC()
	}
	for _, n := range d.notifiers {
		if err := n.Send(ctx, alert); err != nil {
			log.Printf("[notify] %s error: %v", n.Name(), err)
		}
	}
}
