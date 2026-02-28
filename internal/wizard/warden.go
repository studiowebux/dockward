package wizard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/studiowebux/dockward/internal/warden"
)

// RunWarden launches the interactive config wizard for a warden JSON config.
// If the file exists it is loaded first. If not, a blank config is built.
func RunWarden(path string) error {
	s := newScanner()

	var cfg warden.WardenConfig
	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path) // #nosec G304
		if err != nil {
			return fmt.Errorf("read warden config: %w", err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("parse warden config: %w", err)
		}
		fmt.Printf("Loaded existing warden config: %s\n", path)
	} else {
		fmt.Printf("Creating new warden config: %s\n", path)
	}

	// Apply defaults so prompts show sensible values for new configs.
	if cfg.API.Port == "" {
		cfg.API.Port = "8080"
	}

	fmt.Println()

	if err := editWardenAPI(s, &cfg.API); err != nil {
		return err
	}
	if err := editAgents(s, &cfg); err != nil {
		return err
	}

	return writeWardenConfig(path, &cfg)
}

// --- Section editors ---

func editWardenAPI(s *bufio.Scanner, a *warden.WardenAPI) error {
	fmt.Println("[API]")
	a.Port = prompt(s, fmt.Sprintf("  Listen port [%s]: ", a.Port), a.Port)
	a.Token = prompt(s, fmt.Sprintf("  Warden token ($ENV_VAR supported) [%s]: ", a.Token), a.Token)
	a.StatePath = prompt(s, fmt.Sprintf("  State file path (persists ring buffer across restarts, leave empty to disable) [%s]: ", a.StatePath), a.StatePath)
	fmt.Println()
	return nil
}

func editAgents(s *bufio.Scanner, cfg *warden.WardenConfig) error {
	for {
		fmt.Printf("[Agents] (%d configured)\n", len(cfg.Agents))
		for i, a := range cfg.Agents {
			fmt.Printf("  [%d] %s  (%s)\n", i+1, a.ID, a.URL)
		}
		fmt.Println()
		fmt.Println("  a) Add agent")
		if len(cfg.Agents) > 0 {
			fmt.Println("  e) Edit agent")
			fmt.Println("  r) Remove agent")
		}
		fmt.Println("  s) Save and exit")
		fmt.Println("  q) Quit without saving")
		fmt.Println()

		choice := strings.ToLower(strings.TrimSpace(prompt(s, "Choice: ", "")))
		fmt.Println()

		switch choice {
		case "a":
			a := warden.AgentConfig{}
			editAgent(s, &a)
			cfg.Agents = append(cfg.Agents, a)

		case "e":
			if len(cfg.Agents) == 0 {
				continue
			}
			raw := prompt(s, fmt.Sprintf("  Agent number (1-%d): ", len(cfg.Agents)), "")
			idx, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || idx < 1 || idx > len(cfg.Agents) {
				fmt.Println("  Invalid selection.")
				continue
			}
			editAgent(s, &cfg.Agents[idx-1])

		case "r":
			if len(cfg.Agents) == 0 {
				continue
			}
			raw := prompt(s, fmt.Sprintf("  Agent number to remove (1-%d): ", len(cfg.Agents)), "")
			idx, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || idx < 1 || idx > len(cfg.Agents) {
				fmt.Println("  Invalid selection.")
				continue
			}
			id := cfg.Agents[idx-1].ID
			cfg.Agents = append(cfg.Agents[:idx-1], cfg.Agents[idx:]...)
			fmt.Printf("  Removed agent %q.\n\n", id)

		case "s":
			return nil

		case "q":
			fmt.Println("Quit — config not saved.")
			os.Exit(0)

		default:
			fmt.Println("  Unknown option.")
		}
	}
}

func editAgent(s *bufio.Scanner, a *warden.AgentConfig) {
	fmt.Println("[Agent]")
	a.ID = prompt(s, fmt.Sprintf("  ID (display name) [%s]: ", a.ID), a.ID)
	a.URL = prompt(s, fmt.Sprintf("  URL (agent base URL for heartbeat, e.g. http://host:9090) [%s]: ", a.URL), a.URL)
	a.Token = prompt(s, fmt.Sprintf("  Token (must match agent push.token, $ENV_VAR supported) [%s]: ", a.Token), a.Token)
	fmt.Println()
}

// --- Output ---

func writeWardenConfig(path string, cfg *warden.WardenConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal warden config: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0644); err != nil { // #nosec G306
		return fmt.Errorf("write warden config: %w", err)
	}

	fmt.Printf("Saved: %s\n", path)
	return nil
}
