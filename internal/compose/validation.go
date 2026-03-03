// Package compose wraps docker compose CLI commands via os/exec.
package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// projectNameRegex enforces strict project name validation: alphanumeric + dash + underscore only, 1-64 chars
	projectNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

// validateProjectName ensures the project name contains only safe characters.
// Prevents command injection via malformed project names.
func validateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("project name cannot be empty")
	}
	if !projectNameRegex.MatchString(name) {
		return fmt.Errorf("project name contains invalid characters: must match ^[a-zA-Z0-9_-]{1,64}$ (got %q)", name)
	}
	return nil
}

// validateFilePath ensures a file path is absolute, exists, and contains no path traversal attempts.
// Prevents directory traversal attacks via malicious compose file paths.
func validateFilePath(path string) error {
	if path == "" {
		return fmt.Errorf("file path cannot be empty")
	}

	// Must be absolute path
	if !filepath.IsAbs(path) {
		return fmt.Errorf("file path must be absolute: %q", path)
	}

	// Clean the path to resolve any .. or . components
	cleanPath := filepath.Clean(path)

	// Check for path traversal attempts (.. components)
	if strings.Contains(path, "..") {
		return fmt.Errorf("file path contains path traversal attempt: %q", path)
	}

	// Verify file exists and is a regular file
	info, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file does not exist: %q", cleanPath)
		}
		return fmt.Errorf("cannot stat file: %q: %w", cleanPath, err)
	}

	// Must be a regular file (not directory, socket, device, etc.)
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file: %q (mode: %v)", cleanPath, info.Mode())
	}

	return nil
}

// validateEnvFilePath validates an environment file path.
// Empty paths are allowed (optional env file).
func validateEnvFilePath(path string) error {
	if path == "" {
		// Empty env file is valid (optional)
		return nil
	}
	return validateFilePath(path)
}

// validateComposeFiles validates all compose file paths in the list.
func validateComposeFiles(files []string) error {
	if len(files) == 0 {
		return fmt.Errorf("at least one compose file is required")
	}

	for i, file := range files {
		if err := validateFilePath(file); err != nil {
			return fmt.Errorf("invalid compose file [%d]: %w", i, err)
		}
	}
	return nil
}