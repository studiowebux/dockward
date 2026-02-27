// Package push forwards audit entries to a remote warden via HTTP.
// The Client satisfies the audit.Pusher interface.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
)

// Client sends audit entries to a warden /ingest endpoint.
type Client struct {
	url     string
	token   string
	machine string
	http    *http.Client
}

// New creates a Client. url is the warden base URL (e.g. "https://warden.example.com"),
// token is the bearer token, machine is the agent identifier shown in the warden UI.
func New(url, token, machine string) *Client {
	return &Client{
		url:     url,
		token:   token,
		machine: machine,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Send POSTs entry to <url>/ingest with a Bearer token header.
// entry.Machine is set to c.machine before sending.
// Callers should invoke Send in a goroutine (fire and forget).
func (c *Client) Send(ctx context.Context, entry audit.Entry) error {
	entry.Machine = c.machine

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("push: marshal entry: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/ingest", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("push: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("push: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("push: warden returned %d", resp.StatusCode)
	}

	return nil
}
