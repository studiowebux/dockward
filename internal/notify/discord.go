package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Discord sends alerts to a Discord channel via webhook.
type Discord struct {
	webhookURL string
	client     *http.Client
}

// NewDiscord creates a Discord notifier.
func NewDiscord(webhookURL string) *Discord {
	return &Discord{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *Discord) Name() string { return "discord" }

func (d *Discord) Send(ctx context.Context, alert Alert) error {
	color := colorForLevel(alert.Level)

	description := alert.Message
	if alert.Reason != "" {
		description += "\nReason: " + alert.Reason
	}
	if alert.OldDigest != "" && alert.NewDigest != "" {
		description += fmt.Sprintf("\nOld: %s\nNew: %s", shortDigest(alert.OldDigest), shortDigest(alert.NewDigest))
	}

	payload := discordPayload{
		Embeds: []discordEmbed{{
			Title:       fmt.Sprintf("[%s] %s: %s", alert.Level, alert.Event, alert.Service),
			Description: description,
			Color:       color,
			Timestamp:   alert.Timestamp.Format(time.RFC3339),
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req) // #nosec G704 -- URL from local config
	if err != nil {
		return fmt.Errorf("discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("discord webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}

type discordPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       int    `json:"color"`
	Timestamp   string `json:"timestamp"`
}

func colorForLevel(level string) int {
	switch level {
	case LevelCritical:
		return 15158332 // red
	case LevelWarning:
		return 16776960 // yellow
	default:
		return 3066993 // green
	}
}

func shortDigest(digest string) string {
	if len(digest) > 19 {
		return digest[:19]
	}
	return digest
}
