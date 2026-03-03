// Package config handles loading and parsing the watcher configuration.
// Uses JSON format (stdlib encoding/json, zero external dependencies).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Push defines optional warden push settings for agent mode.
type Push struct {
	WardenURL string `json:"warden_url"` // empty = disabled
	Token     string `json:"token"`      // bearer token; $ENV_VAR expansion supported
	MachineID string `json:"machine_id"` // identifier shown in warden UI
}

// Config is the top-level configuration.
type Config struct {
	Runtime         string        `json:"runtime"`        // Container runtime: "docker" or "podman", default: "docker"
	Registry        Registry      `json:"registry"`
	API             API           `json:"api"`
	Audit           Audit         `json:"audit"`
	Monitor         Monitor       `json:"monitor"`
	DockerHealth    DockerHealth  `json:"docker_health"`
	Notifications   Notifications `json:"notifications"`
	Push            Push          `json:"push"`
	Services        []Service     `json:"services"`
	InvalidServices []ServiceValidationError `json:"-"` // Services that failed validation (not serialized)
}

// ServiceValidationError records why a service was skipped during validation.
type ServiceValidationError struct {
	Index   int
	Name    string
	Reason  string
	Service Service
}

// Audit defines the audit log file. Empty path disables audit logging.
type Audit struct {
	Path string `json:"path"` // absolute path to JSON Lines log file; empty = disabled
}

// API defines the trigger/metrics HTTP server.
type API struct {
	Port    string `json:"port"`    // default: 9090
	Address string `json:"address"` // default: 127.0.0.1
}

// Registry defines the local Docker registry connection.
type Registry struct {
	URL          string `json:"url"`
	PollInterval int    `json:"poll_interval"` // seconds
	Insecure     bool   `json:"insecure"`      // skip TLS verification for self-signed certs
}

// Monitor controls resource stat collection (CPU, memory).
type Monitor struct {
	StatsInterval int `json:"stats_interval"` // seconds; defaults to registry.poll_interval if unset
}

// DockerHealth controls Docker daemon health checks.
type DockerHealth struct {
	CheckInterval int `json:"check_interval"` // seconds; how often to ping Docker daemon (default: 30)
	Timeout       int `json:"timeout"`        // seconds; timeout for each ping request (default: 5)
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
	Password string `json:"password,omitempty"` // #nosec G117 -- SMTP credential, not a secret leak
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
	Name            string   `json:"name"`
	Images          []string `json:"images,omitempty"`        // Registry images to watch for updates
	Silent          bool     `json:"silent"`                  // Exclude from validation and monitoring (e.g., heal-only with no images)
	ComposeFiles    []string `json:"compose_files,omitempty"` // Ordered list of compose files; merged left to right
	ComposeProject  string   `json:"compose_project"`
	ContainerName   string   `json:"container_name,omitempty"`
	EnvFile         string   `json:"env_file,omitempty"`
	AutoUpdate      bool     `json:"auto_update"`
	AutoStart       bool     `json:"auto_start"`
	AutoHeal        bool     `json:"auto_heal"`
	ComposeWatch    bool     `json:"compose_watch"`    // re-deploy on compose file content change (no pull)
	CPUThreshold    float64  `json:"cpu_threshold"`    // alert when CPU % exceeds this value; 0 = disabled
	MemoryThreshold float64  `json:"memory_threshold"` // alert when memory % exceeds this value; 0 = disabled
	HealthGrace     int      `json:"health_grace"`     // seconds, default 60
	HealCooldown    int      `json:"heal_cooldown"`    // seconds, default 300
	HealMaxRestarts int      `json:"heal_max_restarts"` // max consecutive failed restarts before giving up, default 3
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

	// Log warnings for invalid services (non-fatal)
	if len(cfg.InvalidServices) > 0 {
		fmt.Fprintf(os.Stderr, "[config] WARNING: %d service(s) failed validation and will be skipped:\n", len(cfg.InvalidServices))
		for _, inv := range cfg.InvalidServices {
			fmt.Fprintf(os.Stderr, "  - service[%d] %q: %s\n", inv.Index, inv.Name, inv.Reason)
		}
	}

	// Expand environment variables in webhook headers
	for i := range cfg.Notifications.Webhooks {
		for k, v := range cfg.Notifications.Webhooks[i].Headers {
			cfg.Notifications.Webhooks[i].Headers[k] = os.ExpandEnv(v)
		}
	}

	// Expand environment variables in push config.
	cfg.Push.WardenURL = os.ExpandEnv(cfg.Push.WardenURL)
	cfg.Push.Token = os.ExpandEnv(cfg.Push.Token)

	return cfg, nil
}

func (c *Config) setDefaults() {
	if c.Runtime == "" {
		c.Runtime = "docker" // Default to docker for backward compatibility
	}
	if c.Registry.URL == "" {
		c.Registry.URL = "http://localhost:5000"
	}
	if c.Registry.PollInterval <= 0 {
		c.Registry.PollInterval = 300
	}
	if c.Monitor.StatsInterval <= 0 {
		c.Monitor.StatsInterval = c.Registry.PollInterval
	}
	if c.DockerHealth.CheckInterval <= 0 {
		c.DockerHealth.CheckInterval = 30
	}
	if c.DockerHealth.Timeout <= 0 {
		c.DockerHealth.Timeout = 5
	}
	if c.API.Port == "" {
		c.API.Port = "9090"
	}
	if c.API.Address == "" {
		c.API.Address = "127.0.0.1"
	}
	for i := range c.Services {
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
	// Validate runtime is either docker or podman (FATAL - cannot proceed without valid runtime)
	if c.Runtime != "docker" && c.Runtime != "podman" {
		return fmt.Errorf("runtime must be 'docker' or 'podman', got %q", c.Runtime)
	}

	// Compile regex for project name validation once
	projectNameRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

	// Collect valid services and track invalid ones (non-fatal)
	validServices := make([]Service, 0, len(c.Services))
	c.InvalidServices = []ServiceValidationError{}

	for i, svc := range c.Services {
		// Silent services skip validation entirely
		if svc.Silent {
			validServices = append(validServices, svc)
			continue
		}

		// Helper to mark service as invalid
		markInvalid := func(reason string) {
			c.InvalidServices = append(c.InvalidServices, ServiceValidationError{
				Index:   i,
				Name:    svc.Name,
				Reason:  reason,
				Service: svc,
			})
		}

		// Validate service-level requirements
		if svc.Name == "" {
			markInvalid(fmt.Sprintf("service[%d]: name is required", i))
			continue
		}

		// Validate compose project name for security
		if svc.ComposeProject != "" {
			if !projectNameRegex.MatchString(svc.ComposeProject) {
				markInvalid(fmt.Sprintf("compose_project contains invalid characters: must match ^[a-zA-Z0-9_-]{1,64}$ (got %q)", svc.ComposeProject))
				continue
			}
		}

		// Validate compose files with security checks
		composeFilesValid := true
		for j, cf := range svc.ComposeFiles {
			// Must be absolute path
			if !filepath.IsAbs(cf) {
				markInvalid(fmt.Sprintf("compose_file[%d] must be absolute path: %q", j, cf))
				composeFilesValid = false
				break
			}

			// Check for path traversal attempts (SECURITY: always fatal)
			if strings.Contains(cf, "..") {
				markInvalid(fmt.Sprintf("compose_file[%d] contains path traversal attempt: %q", j, cf))
				composeFilesValid = false
				break
			}

			// Clean the path
			cleanPath := filepath.Clean(cf)

			// Verify file exists and is regular file
			info, err := os.Stat(cleanPath)
			if err != nil {
				if os.IsNotExist(err) {
					markInvalid(fmt.Sprintf("compose_file[%d] not found: %q", j, cleanPath))
				} else {
					markInvalid(fmt.Sprintf("compose_file[%d] stat error: %q: %v", j, cleanPath, err))
				}
				composeFilesValid = false
				break
			}

			if !info.Mode().IsRegular() {
				markInvalid(fmt.Sprintf("compose_file[%d] is not a regular file: %q (mode: %v)", j, cleanPath, info.Mode()))
				composeFilesValid = false
				break
			}
		}
		if !composeFilesValid {
			continue
		}

		// Validate env file with security checks
		if svc.EnvFile != "" {
			// Must be absolute path
			if !filepath.IsAbs(svc.EnvFile) {
				markInvalid(fmt.Sprintf("env_file must be absolute path: %q", svc.EnvFile))
				continue
			}

			// Check for path traversal attempts (SECURITY: always fatal)
			if strings.Contains(svc.EnvFile, "..") {
				markInvalid(fmt.Sprintf("env_file contains path traversal attempt: %q", svc.EnvFile))
				continue
			}

			// Clean the path
			cleanPath := filepath.Clean(svc.EnvFile)

			// Verify file exists and is regular file
			info, err := os.Stat(cleanPath)
			if err != nil {
				if os.IsNotExist(err) {
					markInvalid(fmt.Sprintf("env_file not found: %q", cleanPath))
				} else {
					markInvalid(fmt.Sprintf("env_file stat error: %q: %v", cleanPath, err))
				}
				continue
			}

			if !info.Mode().IsRegular() {
				markInvalid(fmt.Sprintf("env_file is not a regular file: %q (mode: %v)", cleanPath, info.Mode()))
				continue
			}
		}

		// Validate feature requirements
		if svc.AutoUpdate {
			if len(svc.Images) == 0 {
				markInvalid("images is required when auto_update is true")
				continue
			}
			if len(svc.ComposeFiles) == 0 {
				markInvalid("compose_files is required when auto_update is true")
				continue
			}
			if svc.ComposeProject == "" {
				markInvalid("compose_project is required when auto_update is true")
				continue
			}
		}
		if svc.AutoHeal && svc.ComposeProject == "" && svc.ContainerName == "" {
			markInvalid("compose_project or container_name is required when auto_heal is true")
			continue
		}

		// Validate thresholds
		if svc.CPUThreshold < 0 || svc.CPUThreshold > 100 {
			markInvalid(fmt.Sprintf("cpu_threshold must be between 0-100, got %.0f", svc.CPUThreshold))
			continue
		}
		if svc.MemoryThreshold < 0 || svc.MemoryThreshold > 100 {
			markInvalid(fmt.Sprintf("memory_threshold must be between 0-100, got %.0f", svc.MemoryThreshold))
			continue
		}

		// Validate timing values
		if svc.HealthGrace < 0 {
			markInvalid("health_grace cannot be negative")
			continue
		}
		if svc.HealCooldown < 0 {
			markInvalid("heal_cooldown cannot be negative")
			continue
		}
		if svc.HealMaxRestarts < 0 {
			markInvalid("heal_max_restarts cannot be negative")
			continue
		}

		// Service passed all validation checks
		validServices = append(validServices, svc)
	}

	// Replace services list with only valid ones
	c.Services = validServices

	// Validate global settings
	if c.Registry.PollInterval < 10 {
		return fmt.Errorf("registry.poll_interval must be at least 10 seconds, got %d", c.Registry.PollInterval)
	}
	if c.Registry.PollInterval > 86400 {
		return fmt.Errorf("registry.poll_interval cannot exceed 86400 seconds (24 hours), got %d", c.Registry.PollInterval)
	}
	if c.Monitor.StatsInterval < 5 && c.Monitor.StatsInterval != 0 {
		return fmt.Errorf("monitor.stats_interval must be at least 5 seconds or 0 (disabled), got %d", c.Monitor.StatsInterval)
	}

	// Validate Docker health check settings
	if c.DockerHealth.CheckInterval < 5 {
		return fmt.Errorf("docker_health.check_interval must be at least 5 seconds, got %d", c.DockerHealth.CheckInterval)
	}
	if c.DockerHealth.CheckInterval > 3600 {
		return fmt.Errorf("docker_health.check_interval cannot exceed 3600 seconds (1 hour), got %d", c.DockerHealth.CheckInterval)
	}
	if c.DockerHealth.Timeout < 1 {
		return fmt.Errorf("docker_health.timeout must be at least 1 second, got %d", c.DockerHealth.Timeout)
	}
	if c.DockerHealth.Timeout > 30 {
		return fmt.Errorf("docker_health.timeout cannot exceed 30 seconds, got %d", c.DockerHealth.Timeout)
	}
	if c.DockerHealth.Timeout >= c.DockerHealth.CheckInterval {
		return fmt.Errorf("docker_health.timeout (%ds) must be less than check_interval (%ds)", c.DockerHealth.Timeout, c.DockerHealth.CheckInterval)
	}

	// Validate API port
	if c.API.Port != "" {
		if port, err := strconv.Atoi(c.API.Port); err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("api.port must be a valid port number (1-65535), got %q", c.API.Port)
		}
	}

	return nil
}
