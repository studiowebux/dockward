// Package compose wraps docker compose CLI commands via os/exec.
package compose

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// Pull runs "docker compose -p <project> -f <file> pull" for the given compose file.
func Pull(ctx context.Context, composeFile, project string) error {
	return run(ctx, composeFile, project, "pull")
}

// Up runs "docker compose -p <project> -f <file> up -d" for the given compose file.
func Up(ctx context.Context, composeFile, project string) error {
	return run(ctx, composeFile, project, "up", "-d")
}

func run(ctx context.Context, composeFile, project string, args ...string) error {
	cmdArgs := []string{"compose", "-p", project, "-f", composeFile}
	cmdArgs = append(cmdArgs, args...)

	log.Printf("[compose] docker %s", strings.Join(cmdArgs, " "))

	cmd := exec.CommandContext(ctx, "docker", cmdArgs...) // #nosec G204 -- args from local config file, not user input
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose %s: %w\noutput: %s", args[0], err, string(output))
	}
	return nil
}
