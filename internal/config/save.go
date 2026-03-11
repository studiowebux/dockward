package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ApplyDefaults fills in default values for missing or zero fields.
// Call this after constructing or modifying a Config outside of Load.
func (c *Config) ApplyDefaults() {
	c.setDefaults()
}

// Save writes the current config to path atomically (write to temp, then rename).
// InvalidServices is excluded (json:"-") — only valid services are persisted.
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dockward-config-*.tmp") // #nosec G306
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName) // #nosec G104 — best-effort cleanup
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName) // #nosec G104 — best-effort cleanup
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName) // #nosec G104 — best-effort cleanup
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}
