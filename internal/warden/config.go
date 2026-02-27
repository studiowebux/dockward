// Package warden implements the central aggregator that collects audit entries
// from multiple agents, stores them in a ring buffer, fans out via SSE, and
// serves a multi-machine dashboard.
package warden

import (
	"encoding/json"
	"fmt"
	"os"
)

// WardenConfig is the top-level warden configuration.
type WardenConfig struct {
	API    WardenAPI     `json:"api"`
	Agents []AgentConfig `json:"agents"`
}

// WardenAPI defines the HTTP server settings for the warden.
type WardenAPI struct {
	Port  string `json:"port"`  // default "8080"
	Token string `json:"token"` // bearer token for browser auth; $ENV_VAR expansion supported
}

// AgentConfig describes one monitored agent.
type AgentConfig struct {
	ID    string `json:"id"`    // display name shown in the warden UI
	URL   string `json:"url"`   // agent base URL used for heartbeat polling (GET /health)
	Token string `json:"token"` // token the agent uses when POSTing to /ingest
}

// LoadWarden reads and parses a warden JSON config file.
func LoadWarden(path string) (*WardenConfig, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path from CLI flag, not network input
	if err != nil {
		return nil, fmt.Errorf("read warden config: %w", err)
	}

	cfg := &WardenConfig{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse warden config: %w", err)
	}

	cfg.setDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate warden config: %w", err)
	}

	// Expand environment variables.
	cfg.API.Token = os.ExpandEnv(cfg.API.Token)
	for i := range cfg.Agents {
		cfg.Agents[i].Token = os.ExpandEnv(cfg.Agents[i].Token)
		cfg.Agents[i].URL = os.ExpandEnv(cfg.Agents[i].URL)
	}

	return cfg, nil
}

func (c *WardenConfig) setDefaults() {
	if c.API.Port == "" {
		c.API.Port = "8080"
	}
}

func (c *WardenConfig) validate() error {
	if c.API.Token == "" {
		return fmt.Errorf("api.token is required")
	}
	for i, a := range c.Agents {
		if a.ID == "" {
			return fmt.Errorf("agents[%d]: id is required", i)
		}
		if a.URL == "" {
			return fmt.Errorf("agents[%d] %q: url is required", i, a.ID)
		}
		if a.Token == "" {
			return fmt.Errorf("agents[%d] %q: token is required", i, a.ID)
		}
	}
	return nil
}
