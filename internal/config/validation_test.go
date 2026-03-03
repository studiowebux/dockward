package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigValidation_Runtime(t *testing.T) {
	tests := []struct {
		name    string
		runtime string
		wantErr bool
	}{
		{"valid docker", "docker", false},
		{"valid podman", "podman", false},
		{"invalid runtime", "containerd", true},
		{"empty defaults to docker", "", false}, // setDefaults() will set it
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Runtime: tt.runtime,
				Registry: Registry{
					URL:          "http://localhost:5000",
					PollInterval: 300,
				},
				API: API{
					Port: "9090",
				},
			}

			cfg.setDefaults()
			err := cfg.validate()

			if (err != nil) != tt.wantErr {
				t.Errorf("validate() with runtime=%q: error = %v, wantErr %v", tt.runtime, err, tt.wantErr)
			}
		})
	}
}

func TestConfigValidation_ProjectName(t *testing.T) {
	tmpDir := t.TempDir()
	validComposeFile := filepath.Join(tmpDir, "compose.yml")
	if err := os.WriteFile(validComposeFile, []byte("version: '3'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		projectName string
		wantErr     bool
		errContains string
	}{
		{"valid project name", "my-project_123", false, ""},
		{"empty project name", "", false, ""}, // Only required if auto_update is true
		{"invalid chars - semicolon", "project;ls", true, "invalid characters"},
		{"invalid chars - space", "my project", true, "invalid characters"},
		{"invalid chars - slash", "my/project", true, "invalid characters"},
		{"invalid chars - dots", "../etc", true, "invalid characters"},
		{"too long", "a1234567890123456789012345678901234567890123456789012345678901234", true, "invalid characters"}, // 65 chars
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Runtime: "docker",
				Registry: Registry{
					URL:          "http://localhost:5000",
					PollInterval: 300,
				},
				API: API{
					Port: "9090",
				},
				Services: []Service{
					{
						Name:           "test-service",
						ComposeProject: tt.projectName,
						ComposeFiles:   []string{validComposeFile},
						Images:         []string{"test:latest"},
						AutoUpdate:     tt.projectName != "", // Only validate if project name is set
					},
				},
			}

			cfg.setDefaults()
			err := cfg.validate()

			// After config validation refactor: invalid services are collected, not fatal errors
			if tt.wantErr {
				// Should have invalid services
				if len(cfg.InvalidServices) == 0 {
					t.Errorf("validate() with projectName=%q: expected invalid service, got none", tt.projectName)
				}
				if len(cfg.InvalidServices) > 0 && tt.errContains != "" {
					if !contains(cfg.InvalidServices[0].Reason, tt.errContains) {
						t.Errorf("validate() reason = %q, want to contain %q", cfg.InvalidServices[0].Reason, tt.errContains)
					}
				}
			} else {
				// Should have no errors and no invalid services
				if err != nil {
					t.Errorf("validate() with projectName=%q: unexpected error = %v", tt.projectName, err)
				}
				if len(cfg.InvalidServices) > 0 {
					t.Errorf("validate() with projectName=%q: unexpected invalid services = %v", tt.projectName, cfg.InvalidServices)
				}
			}
		})
	}
}

func TestConfigValidation_ComposePaths(t *testing.T) {
	tmpDir := t.TempDir()

	// Create valid files
	validFile := filepath.Join(tmpDir, "compose.yml")
	if err := os.WriteFile(validFile, []byte("version: '3'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a directory (not a regular file)
	dirPath := filepath.Join(tmpDir, "mydir")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		composeFiles []string
		wantErr      bool
		errContains  string
	}{
		{"valid absolute path", []string{validFile}, false, ""},
		{"relative path", []string{"compose.yml"}, true, "must be absolute"},
		{"path traversal", []string{"/etc/../etc/passwd"}, true, "path traversal"},
		{"non-existent", []string{"/does/not/exist.yml"}, true, "not found"},
		{"directory not file", []string{dirPath}, true, "not a regular file"},
		{"multiple files mixed", []string{validFile, "/does/not/exist.yml"}, true, "not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Runtime: "docker",
				Registry: Registry{
					URL:          "http://localhost:5000",
					PollInterval: 300,
				},
				API: API{
					Port: "9090",
				},
				Services: []Service{
					{
						Name:           "test-service",
						ComposeProject: "test",
						ComposeFiles:   tt.composeFiles,
						Images:         []string{"test:latest"},
						AutoUpdate:     true,
					},
				},
			}

			cfg.setDefaults()
			err := cfg.validate()

			// After config validation refactor: invalid services are collected, not fatal errors
			if tt.wantErr {
				// Should have invalid services
				if len(cfg.InvalidServices) == 0 {
					t.Errorf("validate() with composeFiles=%v: expected invalid service, got none", tt.composeFiles)
				}
				if len(cfg.InvalidServices) > 0 && tt.errContains != "" {
					if !contains(cfg.InvalidServices[0].Reason, tt.errContains) {
						t.Errorf("validate() reason = %q, want to contain %q", cfg.InvalidServices[0].Reason, tt.errContains)
					}
				}
			} else {
				// Should have no errors and no invalid services
				if err != nil {
					t.Errorf("validate() with composeFiles=%v: unexpected error = %v", tt.composeFiles, err)
				}
				if len(cfg.InvalidServices) > 0 {
					t.Errorf("validate() with composeFiles=%v: unexpected invalid services = %v", tt.composeFiles, cfg.InvalidServices)
				}
			}
		})
	}
}

func TestConfigValidation_EnvFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create valid files
	validComposeFile := filepath.Join(tmpDir, "compose.yml")
	if err := os.WriteFile(validComposeFile, []byte("version: '3'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	validEnvFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(validEnvFile, []byte("KEY=value\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		envFile     string
		wantErr     bool
		errContains string
	}{
		{"empty env file (valid)", "", false, ""},
		{"valid absolute path", validEnvFile, false, ""},
		{"relative path", ".env", true, "must be absolute"},
		{"path traversal", "/etc/../etc/passwd", true, "path traversal"},
		{"non-existent", "/does/not/exist/.env", true, "not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Runtime: "docker",
				Registry: Registry{
					URL:          "http://localhost:5000",
					PollInterval: 300,
				},
				API: API{
					Port: "9090",
				},
				Services: []Service{
					{
						Name:           "test-service",
						ComposeProject: "test",
						ComposeFiles:   []string{validComposeFile},
						EnvFile:        tt.envFile,
						Images:         []string{"test:latest"},
						AutoUpdate:     true,
					},
				},
			}

			cfg.setDefaults()
			err := cfg.validate()

			// After config validation refactor: invalid services are collected, not fatal errors
			if tt.wantErr {
				// Should have invalid services
				if len(cfg.InvalidServices) == 0 {
					t.Errorf("validate() with envFile=%q: expected invalid service, got none", tt.envFile)
				}
				if len(cfg.InvalidServices) > 0 && tt.errContains != "" {
					if !contains(cfg.InvalidServices[0].Reason, tt.errContains) {
						t.Errorf("validate() reason = %q, want to contain %q", cfg.InvalidServices[0].Reason, tt.errContains)
					}
				}
			} else {
				// Should have no errors and no invalid services
				if err != nil {
					t.Errorf("validate() with envFile=%q: unexpected error = %v", tt.envFile, err)
				}
				if len(cfg.InvalidServices) > 0 {
					t.Errorf("validate() with envFile=%q: unexpected invalid services = %v", tt.envFile, cfg.InvalidServices)
				}
			}
		})
	}
}

func TestConfigLoad_MaliciousConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid compose file for reference
	validComposeFile := filepath.Join(tmpDir, "compose.yml")
	if err := os.WriteFile(validComposeFile, []byte("version: '3'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		configJSON  string
		wantErr     bool
		errContains string
	}{
		{
			name: "command injection in project name",
			configJSON: `{
				"runtime": "docker",
				"registry": {"url": "http://localhost:5000", "poll_interval": 300},
				"api": {"port": "9090"},
				"services": [{
					"name": "test",
					"compose_project": "project;rm -rf /",
					"compose_files": ["` + validComposeFile + `"],
					"images": ["test:latest"],
					"auto_update": true
				}]
			}`,
			wantErr:     true,
			errContains: "invalid characters",
		},
		{
			name: "path traversal in compose file",
			configJSON: `{
				"runtime": "docker",
				"registry": {"url": "http://localhost:5000", "poll_interval": 300},
				"api": {"port": "9090"},
				"services": [{
					"name": "test",
					"compose_project": "test",
					"compose_files": ["/etc/../etc/passwd"],
					"images": ["test:latest"],
					"auto_update": true
				}]
			}`,
			wantErr:     true,
			errContains: "path traversal",
		},
		{
			name: "invalid runtime",
			configJSON: `{
				"runtime": "runc",
				"registry": {"url": "http://localhost:5000", "poll_interval": 300},
				"api": {"port": "9090"}
			}`,
			wantErr:     true,
			errContains: "runtime must be 'docker' or 'podman'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configFile := filepath.Join(tmpDir, "config.json")
			if err := os.WriteFile(configFile, []byte(tt.configJSON), 0644); err != nil {
				t.Fatal(err)
			}

			cfg, err := Load(configFile)

			// After config validation refactor: service-level issues are non-fatal
			// Only runtime validation is still fatal
			if tt.name == "invalid runtime" {
				// Runtime validation is still fatal
				if (err != nil) != tt.wantErr {
					t.Errorf("Load() error = %v, wantErr %v", err, tt.wantErr)
				}
				if err != nil && tt.errContains != "" {
					if !contains(err.Error(), tt.errContains) {
						t.Errorf("Load() error = %q, want to contain %q", err.Error(), tt.errContains)
					}
				}
			} else {
				// Service validation errors are now non-fatal
				if err != nil {
					t.Errorf("Load() unexpected error = %v", err)
				}
				if cfg != nil && len(cfg.InvalidServices) == 0 {
					t.Errorf("Load() expected invalid services, got none")
				}
				if cfg != nil && len(cfg.InvalidServices) > 0 && tt.errContains != "" {
					if !contains(cfg.InvalidServices[0].Reason, tt.errContains) {
						t.Errorf("Load() invalid service reason = %q, want to contain %q", cfg.InvalidServices[0].Reason, tt.errContains)
					}
				}
			}
		})
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || len(s) > len(substr) && containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 1; i < len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}