package docker

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// Container represents a running Docker container (from list endpoint).
type Container struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
}

// ContainerInspect is the full container detail (from inspect endpoint).
type ContainerInspect struct {
	ID     string          `json:"Id"`
	Name   string          `json:"Name"`
	Image  string          `json:"Image"`
	State  ContainerState  `json:"State"`
	Config ContainerConfig `json:"Config"`
}

// ContainerState holds the runtime state including health.
type ContainerState struct {
	Status  string        `json:"Status"` // running, exited, restarting, etc.
	Running bool          `json:"Running"`
	Health  *HealthState  `json:"Health,omitempty"`
}

// HealthState is the container's health check status.
type HealthState struct {
	Status        string       `json:"Status"` // healthy, unhealthy, starting
	FailingStreak int          `json:"FailingStreak"`
	Log           []HealthLog  `json:"Log"`
}

// HealthLog is a single health check result.
type HealthLog struct {
	Start    time.Time `json:"Start"`
	End      time.Time `json:"End"`
	ExitCode int       `json:"ExitCode"`
	Output   string    `json:"Output"`
}

// ContainerConfig holds the container's configuration.
type ContainerConfig struct {
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
}

// ListContainers returns all running containers.
func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	data, err := c.get(ctx, "/containers/json")
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var containers []Container
	if err := decodeJSON(data, &containers); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	return containers, nil
}

// ListContainersByProject returns containers matching a compose project label.
// Uses Docker API label filter: com.docker.compose.project=<project>.
func (c *Client) ListContainersByProject(ctx context.Context, project string) ([]Container, error) {
	filter := url.QueryEscape(fmt.Sprintf(`{"label":["com.docker.compose.project=%s"]}`, project))
	data, err := c.get(ctx, "/containers/json?filters="+filter)
	if err != nil {
		return nil, fmt.Errorf("list containers by project %s: %w", project, err)
	}
	var containers []Container
	if err := decodeJSON(data, &containers); err != nil {
		return nil, fmt.Errorf("decode containers by project %s: %w", project, err)
	}
	return containers, nil
}

// InspectContainer returns full details for a single container.
func (c *Client) InspectContainer(ctx context.Context, id string) (*ContainerInspect, error) {
	data, err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/json")
	if err != nil {
		return nil, fmt.Errorf("inspect container %s: %w", id, err)
	}
	var info ContainerInspect
	if err := decodeJSON(data, &info); err != nil {
		return nil, fmt.Errorf("decode inspect %s: %w", id, err)
	}
	return &info, nil
}

// RestartContainer restarts a container with a timeout in seconds.
func (c *Client) RestartContainer(ctx context.Context, id string, timeoutSec int) error {
	path := fmt.Sprintf("/containers/%s/restart?t=%d", url.PathEscape(id), timeoutSec)
	_, err := c.post(ctx, path, "")
	if err != nil {
		return fmt.Errorf("restart container %s: %w", id, err)
	}
	return nil
}

// StopContainer stops a running container.
func (c *Client) StopContainer(ctx context.Context, id string, timeoutSec int) error {
	path := fmt.Sprintf("/containers/%s/stop?t=%d", url.PathEscape(id), timeoutSec)
	_, err := c.post(ctx, path, "")
	if err != nil {
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	return nil
}

// ContainerName returns the clean name (without leading slash).
func (ci *ContainerInspect) ContainerName() string {
	if len(ci.Name) > 0 && ci.Name[0] == '/' {
		return ci.Name[1:]
	}
	return ci.Name
}

// LastHealthOutput returns the output from the most recent health check log entry.
func (ci *ContainerInspect) LastHealthOutput() string {
	if ci.State.Health == nil || len(ci.State.Health.Log) == 0 {
		return ""
	}
	last := ci.State.Health.Log[len(ci.State.Health.Log)-1]
	return last.Output
}
