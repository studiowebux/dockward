// Package docker provides a minimal Docker Engine API client
// that communicates over the Unix socket using net/http.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	apiVersion = "v1.45"
	socketPath = "/var/run/docker.sock"
)

// Client communicates with the Docker Engine API over a Unix socket.
type Client struct {
	http *http.Client
}

// NewClient creates a Docker API client connected to the local socket.
func NewClient() *Client {
	dialer := &net.Dialer{}
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socketPath)
				},
			},
			Timeout: 30 * time.Second,
		},
	}
}

// newStreamClient returns a client with no timeout for long-lived streaming connections.
func newStreamClient() *http.Client {
	dialer := &net.Dialer{}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func (c *Client) url(path string) string {
	return fmt.Sprintf("http://localhost/%s%s", apiVersion, path)
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req) // #nosec G704 -- unix socket only, no external network
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("docker API %s: %d %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *Client) post(ctx context.Context, path string, payload string) ([]byte, error) {
	var bodyReader io.Reader
	if payload != "" {
		bodyReader = strings.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), bodyReader)
	if err != nil {
		return nil, err
	}
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req) // #nosec G704 -- unix socket only, no external network
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("docker API POST %s: %d %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *Client) delete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url(path), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req) // #nosec G704 -- unix socket only, no external network
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // Drain body before close.
	if resp.StatusCode >= 400 {
		return fmt.Errorf("docker API DELETE %s: %d", path, resp.StatusCode)
	}
	return nil
}

// decodeJSON unmarshals JSON data into the target.
func decodeJSON(data []byte, target any) error {
	return json.Unmarshal(data, target)
}
