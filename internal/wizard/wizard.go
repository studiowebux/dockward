// Package wizard provides an interactive guided CLI for creating and editing
// dockward config files. Uses only stdlib — no external dependencies.
package wizard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/studiowebux/dockward/internal/config"
)

// Run launches the interactive config wizard for the given path.
// If the file exists it is loaded and the user can edit it.
// If not, a fresh config is built from scratch.
func Run(path string) error {
	s := newScanner()

	var cfg *config.Config
	if _, err := os.Stat(path); err == nil {
		existing, err := config.Load(path)
		if err != nil {
			return fmt.Errorf("load existing config: %w", err)
		}
		cfg = existing
		fmt.Printf("Loaded existing config: %s\n", path)
	} else {
		cfg = &config.Config{}
		fmt.Printf("Creating new config: %s\n", path)
	}

	fmt.Println()

	if err := editRegistry(s, &cfg.Registry); err != nil {
		return err
	}
	if err := editAPI(s, &cfg.API); err != nil {
		return err
	}
	if err := editAudit(s, &cfg.Audit); err != nil {
		return err
	}
	if err := editPush(s, &cfg.Push); err != nil {
		return err
	}
	if err := editNotifications(s, &cfg.Notifications); err != nil {
		return err
	}
	if err := editServices(s, cfg); err != nil {
		return err
	}

	return writeConfig(path, cfg)
}

// --- Section editors ---

func editRegistry(s *bufio.Scanner, r *config.Registry) error {
	fmt.Println("[Registry]")

	if r.URL == "" {
		r.URL = "http://localhost:5000"
	}
	r.URL = prompt(s, fmt.Sprintf("  Registry URL [%s]: ", r.URL), r.URL)

	if r.PollInterval <= 0 {
		r.PollInterval = 300
	}
	raw := prompt(s, fmt.Sprintf("  Poll interval in seconds [%d]: ", r.PollInterval), strconv.Itoa(r.PollInterval))
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		r.PollInterval = v
	}

	fmt.Println()
	return nil
}

func editAPI(s *bufio.Scanner, a *config.API) error {
	fmt.Println("[API]")

	if a.Port == "" {
		a.Port = "9090"
	}
	a.Port = prompt(s, fmt.Sprintf("  Port [%s]: ", a.Port), a.Port)

	fmt.Println()
	return nil
}

func editAudit(s *bufio.Scanner, a *config.Audit) error {
	fmt.Println("[Audit]")
	a.Path = prompt(s, fmt.Sprintf("  Audit log path (leave empty to disable) [%s]: ", a.Path), a.Path)
	fmt.Println()
	return nil
}

func editPush(s *bufio.Scanner, p *config.Push) error {
	fmt.Println("[Push] (forward audit entries to a central warden)")

	p.WardenURL = prompt(s, fmt.Sprintf("  Warden URL (leave empty to disable) [%s]: ", p.WardenURL), p.WardenURL)
	if p.WardenURL != "" {
		p.Token = prompt(s, fmt.Sprintf("  Push token ($ENV_VAR supported) [%s]: ", p.Token), p.Token)
		p.MachineID = prompt(s, fmt.Sprintf("  Machine ID (label shown in warden UI) [%s]: ", p.MachineID), p.MachineID)
	}

	fmt.Println()
	return nil
}

func editNotifications(s *bufio.Scanner, n *config.Notifications) error {
	fmt.Println("[Notifications]")

	// Discord
	existing := ""
	if n.Discord != nil {
		existing = n.Discord.WebhookURL
	}
	label := "  Discord webhook URL (leave empty to disable)"
	if existing != "" {
		label += fmt.Sprintf(" [%s]", existing)
	}
	label += ": "
	url := prompt(s, label, existing)
	if url != "" {
		n.Discord = &config.Discord{WebhookURL: url}
	} else {
		n.Discord = nil
	}

	// SMTP
	smtpEnabled := n.SMTP != nil && n.SMTP.Host != ""
	smtpDefault := "N"
	if smtpEnabled {
		smtpDefault = "y"
	}
	if confirmYN(s, fmt.Sprintf("  Configure SMTP? (%s): ", smtpDefault), smtpEnabled) {
		if n.SMTP == nil {
			n.SMTP = &config.SMTP{}
		}
		if err := editSMTP(s, n.SMTP); err != nil {
			return err
		}
	} else {
		n.SMTP = nil
	}

	fmt.Println()
	return nil
}

func editSMTP(s *bufio.Scanner, m *config.SMTP) error {
	m.Host = prompt(s, fmt.Sprintf("    SMTP host [%s]: ", m.Host), m.Host)
	portStr := strconv.Itoa(m.Port)
	if m.Port == 0 {
		portStr = "587"
	}
	raw := prompt(s, fmt.Sprintf("    SMTP port [%s]: ", portStr), portStr)
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		m.Port = v
	}
	m.From = prompt(s, fmt.Sprintf("    From address [%s]: ", m.From), m.From)
	m.To = prompt(s, fmt.Sprintf("    To address [%s]: ", m.To), m.To)
	m.Username = prompt(s, fmt.Sprintf("    Username (leave empty to skip) [%s]: ", m.Username), m.Username)
	if m.Username != "" {
		m.Password = prompt(s, "    Password: ", m.Password)
	}
	return nil
}

func editServices(s *bufio.Scanner, cfg *config.Config) error {
	for {
		fmt.Printf("[Services] (%d configured)\n", len(cfg.Services))
		for i, svc := range cfg.Services {
			flags := serviceFlags(svc)
			fmt.Printf("  [%d] %s  (%s)\n", i+1, svc.Name, flags)
		}
		fmt.Println()
		fmt.Println("  a) Add service")
		if len(cfg.Services) > 0 {
			fmt.Println("  e) Edit service")
			fmt.Println("  r) Remove service")
		}
		fmt.Println("  s) Save and exit")
		fmt.Println("  q) Quit without saving")
		fmt.Println()

		choice := strings.ToLower(strings.TrimSpace(prompt(s, "Choice: ", "")))
		fmt.Println()

		switch choice {
		case "a":
			svc := config.Service{}
			if err := editService(s, &svc); err != nil {
				return err
			}
			cfg.Services = append(cfg.Services, svc)

		case "e":
			if len(cfg.Services) == 0 {
				continue
			}
			raw := prompt(s, fmt.Sprintf("  Service number (1-%d): ", len(cfg.Services)), "")
			idx, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || idx < 1 || idx > len(cfg.Services) {
				fmt.Println("  Invalid selection.")
				continue
			}
			if err := editService(s, &cfg.Services[idx-1]); err != nil {
				return err
			}

		case "r":
			if len(cfg.Services) == 0 {
				continue
			}
			raw := prompt(s, fmt.Sprintf("  Service number to remove (1-%d): ", len(cfg.Services)), "")
			idx, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || idx < 1 || idx > len(cfg.Services) {
				fmt.Println("  Invalid selection.")
				continue
			}
			name := cfg.Services[idx-1].Name
			cfg.Services = append(cfg.Services[:idx-1], cfg.Services[idx:]...)
			fmt.Printf("  Removed service %q.\n\n", name)

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

func editService(s *bufio.Scanner, svc *config.Service) error {
	fmt.Println("[Service]")

	svc.Name = prompt(s, fmt.Sprintf("  Name [%s]: ", svc.Name), svc.Name)

	// images: show existing as comma-separated, accept comma-separated input
	existingImages := strings.Join(svc.Images, ", ")
	rawImages := prompt(s, fmt.Sprintf("  Images to watch (comma-separated, e.g. myapp:latest) [%s]: ", existingImages), existingImages)
	if rawImages != "" {
		parts := strings.Split(rawImages, ",")
		svc.Images = make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				svc.Images = append(svc.Images, t)
			}
		}
	}

	// compose_files: show existing as comma-separated, accept comma-separated input
	existingFiles := strings.Join(svc.ComposeFiles, ", ")
	raw := prompt(s, fmt.Sprintf("  Compose files (comma-separated paths) [%s]: ", existingFiles), existingFiles)
	if raw != "" {
		parts := strings.Split(raw, ",")
		svc.ComposeFiles = make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				svc.ComposeFiles = append(svc.ComposeFiles, t)
			}
		}
	}

	svc.ComposeProject = prompt(s, fmt.Sprintf("  Compose project name [%s]: ", svc.ComposeProject), svc.ComposeProject)
	svc.ContainerName = prompt(s, fmt.Sprintf("  Container name (optional, for heal-only mode) [%s]: ", svc.ContainerName), svc.ContainerName)
	svc.EnvFile = prompt(s, fmt.Sprintf("  Env file path (optional) [%s]: ", svc.EnvFile), svc.EnvFile)

	fmt.Println()
	fmt.Println("  Behaviour:")
	svc.AutoUpdate = confirmYN(s, fmt.Sprintf("    Auto update (poll registry and deploy on digest change)? (%s): ", boolDefault(svc.AutoUpdate)), svc.AutoUpdate)
	svc.AutoStart = confirmYN(s, fmt.Sprintf("    Auto start (start compose project if no containers running)? (%s): ", boolDefault(svc.AutoStart)), svc.AutoStart)
	svc.AutoHeal = confirmYN(s, fmt.Sprintf("    Auto heal (restart unhealthy/died containers)? (%s): ", boolDefault(svc.AutoHeal)), svc.AutoHeal)
	svc.ComposeWatch = confirmYN(s, fmt.Sprintf("    Compose watch (re-deploy on compose file change, no pull)? (%s): ", boolDefault(svc.ComposeWatch)), svc.ComposeWatch)

	if svc.HealthGrace <= 0 {
		svc.HealthGrace = 60
	}
	raw = prompt(s, fmt.Sprintf("    Health grace period in seconds [%d]: ", svc.HealthGrace), strconv.Itoa(svc.HealthGrace))
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		svc.HealthGrace = v
	}

	if svc.HealCooldown <= 0 {
		svc.HealCooldown = 300
	}
	raw = prompt(s, fmt.Sprintf("    Heal cooldown in seconds [%d]: ", svc.HealCooldown), strconv.Itoa(svc.HealCooldown))
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		svc.HealCooldown = v
	}

	if svc.HealMaxRestarts <= 0 {
		svc.HealMaxRestarts = 3
	}
	raw = prompt(s, fmt.Sprintf("    Max consecutive restart attempts [%d]: ", svc.HealMaxRestarts), strconv.Itoa(svc.HealMaxRestarts))
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		svc.HealMaxRestarts = v
	}

	fmt.Println()
	fmt.Println("  Resource alerts (0 = disabled):")
	raw = prompt(s, fmt.Sprintf("    CPU alert threshold %% [%.0f]: ", svc.CPUThreshold), fmt.Sprintf("%.0f", svc.CPUThreshold))
	if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
		svc.CPUThreshold = v
	}

	raw = prompt(s, fmt.Sprintf("    Memory alert threshold %% [%.0f]: ", svc.MemoryThreshold), fmt.Sprintf("%.0f", svc.MemoryThreshold))
	if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
		svc.MemoryThreshold = v
	}

	fmt.Println()
	return nil
}

// --- Output ---

func writeConfig(path string, cfg *config.Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0644); err != nil { // #nosec G306 -- config file, not a secret
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Printf("Saved: %s\n", path)
	return nil
}

// --- Helpers ---

func newScanner() *bufio.Scanner {
	return bufio.NewScanner(os.Stdin)
}

// prompt prints the label and reads a line. Returns def if the user enters nothing.
func prompt(s *bufio.Scanner, label, def string) string {
	fmt.Print(label)
	if !s.Scan() {
		return def
	}
	line := strings.TrimSpace(s.Text())
	if line == "" {
		return def
	}
	return line
}

// confirmYN prompts for y/N and returns a bool. def is the value returned on empty input.
func confirmYN(s *bufio.Scanner, label string, def bool) bool {
	fmt.Print(label)
	if !s.Scan() {
		return def
	}
	line := strings.ToLower(strings.TrimSpace(s.Text()))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

func boolDefault(v bool) string {
	if v {
		return "Y/n"
	}
	return "y/N"
}

func serviceFlags(svc config.Service) string {
	var parts []string
	if svc.AutoUpdate {
		parts = append(parts, "auto_update")
	}
	if svc.AutoStart {
		parts = append(parts, "auto_start")
	}
	if svc.AutoHeal {
		parts = append(parts, "auto_heal")
	}
	if svc.ComposeWatch {
		parts = append(parts, "compose_watch")
	}
	if svc.CPUThreshold > 0 {
		parts = append(parts, fmt.Sprintf("cpu>%.0f%%", svc.CPUThreshold))
	}
	if svc.MemoryThreshold > 0 {
		parts = append(parts, fmt.Sprintf("mem>%.0f%%", svc.MemoryThreshold))
	}
	if len(parts) == 0 {
		return "monitor only"
	}
	return strings.Join(parts, ", ")
}
