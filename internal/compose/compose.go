// Package compose wraps docker compose CLI commands via os/exec.
package compose

import (
	"bufio"
	"context"
	"fmt"
	"github.com/studiowebux/dockward/internal/logger"
	"os"
	"os/exec"
	"strings"
)

// Pull runs "docker compose -p <project> -f <file>... pull" for the given compose files.
func Pull(ctx context.Context, composeFiles []string, project string, envFile string) error {
	return run(ctx, composeFiles, project, envFile, "pull")
}

// Up runs "docker compose -p <project> -f <file>... up -d" for the given compose files.
func Up(ctx context.Context, composeFiles []string, project string, envFile string) error {
	return run(ctx, composeFiles, project, envFile, "up", "-d")
}

// Restart runs "docker compose down" followed by "docker compose up -d".
// Used to recover stuck containers (created/restarting state).
func Restart(ctx context.Context, composeFiles []string, project string, envFile string) error {
	if err := run(ctx, composeFiles, project, envFile, "down"); err != nil {
		return err
	}
	return run(ctx, composeFiles, project, envFile, "up", "-d")
}

func run(ctx context.Context, composeFiles []string, project string, envFile string, args ...string) error {
	cmdArgs := []string{"compose", "-p", project}
	for _, f := range composeFiles {
		cmdArgs = append(cmdArgs, "-f", f)
	}
	cmdArgs = append(cmdArgs, args...)

	logger.Printf("[compose] docker %s", strings.Join(cmdArgs, " "))

	cmd := exec.CommandContext(ctx, "docker", cmdArgs...) // #nosec G204 -- args from local config file, not user input

	// Inherit current process env, then overlay vars from env_file
	// so compose file ${VAR} interpolation resolves correctly.
	cmd.Env = os.Environ()
	if envFile != "" {
		extra, err := loadEnvFile(envFile)
		if err != nil {
			return fmt.Errorf("load env_file %s: %w", envFile, err)
		}
		cmd.Env = append(cmd.Env, extra...)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose %s: %w\noutput: %s", args[0], err, string(output))
	}
	return nil
}

// loadEnvFile reads a .env file and returns KEY=VALUE strings.
// Skips blank lines and comments (#). Does not expand variables.
func loadEnvFile(path string) ([]string, error) {
	f, err := os.Open(path) // #nosec G304 -- path from local config file
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var env []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.IndexByte(line, '='); idx >= 0 {
			key := line[:idx]
			val := line[idx+1:]
			val = strings.TrimSpace(val)
			// Strip surrounding quotes (single or double).
			if len(val) >= 2 {
				if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
					val = val[1 : len(val)-1]
				}
			}
			env = append(env, key+"="+val)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return env, nil
}
