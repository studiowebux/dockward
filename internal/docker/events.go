package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"
)

// Event represents a Docker engine event from the event stream.
type Event struct {
	Type   string     `json:"Type"`
	Action string     `json:"Action"`
	Actor  EventActor `json:"Actor"`
	Time   int64      `json:"time"`
}

// EventActor identifies the object that triggered the event.
type EventActor struct {
	ID         string            `json:"ID"`
	Attributes map[string]string `json:"Attributes"`
}

// ContainerName returns the container name from the event attributes.
func (e *Event) ContainerName() string {
	return e.Actor.Attributes["name"]
}

// EventHandler is called for each event received from the stream.
type EventHandler func(event Event)

// StreamEvents opens a long-lived connection to the Docker event stream
// and calls handler for each event. It automatically reconnects on failure.
// Blocks until ctx is cancelled.
func (c *Client) StreamEvents(ctx context.Context, handler EventHandler) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.streamOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		log.Printf("[events] stream disconnected: %v, reconnecting in 5s", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *Client) streamOnce(ctx context.Context, handler EventHandler) error {
	// Filter for container health_status, die, and start events.
	filters := url.QueryEscape(`{"type":["container"],"event":["health_status","die","start"]}`)
	reqURL := fmt.Sprintf("http://localhost/%s/events?filters=%s", apiVersion, filters)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("create event request: %w", err)
	}

	// Use stream client (no timeout) for long-lived connection.
	stream := newStreamClient()
	resp, err := stream.Do(req) // #nosec G704 -- unix socket only, no external network
	if err != nil {
		return fmt.Errorf("connect event stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("event stream HTTP %d", resp.StatusCode)
	}

	decoder := json.NewDecoder(resp.Body)
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		handler(event)
	}
}
