// Package config handles loading and parsing the watcher configuration.
// Uses JSON format (stdlib encoding/json, zero external dependencies).
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the top-level configuration.
type Config struct {
	Registry      Registry      `json:"registry"`
	API           API           `json:"api"`
	Audit         Audit         `json:"audit"`
	Notifications Notifications `json:"notifications"`
	Services      []Service     `json:"services"`
}

// Audit defines the audit log file. Empty path disables audit logging.
type Audit struct {
	Path string `json:"path"` // absolute path to JSON Lines log file; empty = disabled
}

// API defines the trigger/metrics HTTP server.
type API struct {
	Port string `json:"port"` // default: 9090
}

// Registry defines the local Docker registry connection.
type Registry struct {
	URL          string `json:"url"`
	PollInterval int    `json:"poll_interval"` // seconds
}

// Notifications defines all notification channels.
type Notifications struct {
	Discord  *Discord  `json:"discord,omitempty"`
	SMTP     *SMTP     `json:"smtp,omitempty"`
	Webhooks []Webhook `json:"webhooks,omitempty"`
}

// Discord webhook configuration.
type Discord struct {
	WebhookURL string `json:"webhook_url"`
}

// SMTP email configuration.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	From     string `json:"from"`
	To       string `json:"to"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Webhook is a user-defined HTTP webhook with template support.
type Webhook struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// Service defines a watched Docker service.
type Service struct {
	Name           string   `json:"name"`
	Image          string   `json:"image"`
	ComposeFile    string   `json:"compose_file,omitempty"`  // Deprecated: use compose_files
	ComposeFiles   []string `json:"compose_files,omitempty"` // Ordered list of compose files; merged left to right
	ComposeProject string   `json:"compose_project"`
	ContainerName  string   `json:"container_name,omitempty"`
	EnvFile        string   `json:"env_file,omitempty"`
	AutoUpdate     bool     `json:"auto_update"`
	AutoStart      bool     `json:"auto_start"`
	AutoHeal       bool     `json:"auto_heal"`
	HealthGrace    int      `json:"health_grace"`     // seconds, default 60
	HealCooldown   int      `json:"heal_cooldown"`    // seconds, default 300
	HealMaxRestarts int     `json:"heal_max_restarts"` // max consecutive failed restarts before giving up, default 3
}

// Load reads and parses a JSON config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path from CLI flag, not network input
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.setDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	// Expand environment variables in webhook headers
	for i := range cfg.Notifications.Webhooks {
		for k, v := range cfg.Notifications.Webhooks[i].Headers {
			cfg.Notifications.Webhooks[i].Headers[k] = os.ExpandEnv(v)
		}
	}

	return cfg, nil
}

func (c *Config) setDefaults() {
	if c.Registry.URL == "" {
		c.Registry.URL = "http://localhost:5000"
	}
	if c.Registry.PollInterval <= 0 {
		c.Registry.PollInterval = 300
	}
	if c.API.Port == "" {
		c.API.Port = "9090"
	}
	for i := range c.Services {
		// Backward compat: promote deprecated compose_file into compose_files.
		if len(c.Services[i].ComposeFiles) == 0 && c.Services[i].ComposeFile != "" {
			c.Services[i].ComposeFiles = []string{c.Services[i].ComposeFile}
		}
		if c.Services[i].HealthGrace <= 0 {
			c.Services[i].HealthGrace = 60
		}
		if c.Services[i].HealCooldown <= 0 {
			c.Services[i].HealCooldown = 300
		}
		if c.Services[i].HealMaxRestarts <= 0 {
			c.Services[i].HealMaxRestarts = 3
		}
	}
}

func (c *Config) validate() error {
	for i, svc := range c.Services {
		if svc.Name == "" {
			return fmt.Errorf("service[%d]: name is required", i)
		}
		if svc.AutoUpdate {
			if svc.Image == "" {
				return fmt.Errorf("service[%d] %q: image is required when auto_update is true", i, svc.Name)
			}
			if len(svc.ComposeFiles) == 0 {
				return fmt.Errorf("service[%d] %q: compose_files (or deprecated compose_file) is required when auto_update is true", i, svc.Name)
			}
			if svc.ComposeProject == "" {
				return fmt.Errorf("service[%d] %q: compose_project is required when auto_update is true", i, svc.Name)
			}
		}
		if svc.AutoHeal && svc.ComposeProject == "" && svc.ContainerName == "" {
			return fmt.Errorf("service[%d] %q: compose_project or container_name is required when auto_heal is true", i, svc.Name)
		}
	}
	return nil
}
