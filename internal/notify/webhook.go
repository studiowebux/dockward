package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"
)

// WebhookNotifier sends alerts to a user-defined HTTP endpoint.
// URL, headers, and body support Go text/template variables.
type WebhookNotifier struct {
	name     string
	url      string
	method   string
	headers  map[string]string
	bodyTmpl *template.Template
	client   *http.Client
}

// webhookData is the template context passed to body/header templates.
type webhookData struct {
	Service   string
	Event     string
	Message   string
	Reason    string
	OldDigest string
	NewDigest string
	Container string
	Timestamp string
	Level     string
}

// NewWebhook creates a custom webhook notifier.
// Body is a Go text/template string. Headers values may contain $ENV_VAR references
// (expanded at config load time via os.ExpandEnv).
func NewWebhook(name, url, method string, headers map[string]string, body string) (*WebhookNotifier, error) {
	tmpl, err := template.New(name).Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parse webhook body template %q: %w", name, err)
	}

	if method == "" {
		method = http.MethodPost
	}

	return &WebhookNotifier{
		name:     name,
		url:      url,
		method:   method,
		headers:  headers,
		bodyTmpl: tmpl,
		client:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (w *WebhookNotifier) Name() string { return "webhook:" + w.name }

func (w *WebhookNotifier) Send(ctx context.Context, alert Alert) error {
	data := webhookData{
		Service:   alert.Service,
		Event:     alert.Event,
		Message:   alert.Message,
		Reason:    alert.Reason,
		OldDigest: alert.OldDigest,
		NewDigest: alert.NewDigest,
		Container: alert.Container,
		Timestamp: alert.Timestamp.Format(time.RFC3339),
		Level:     alert.Level,
	}

	var body bytes.Buffer
	if err := w.bodyTmpl.Execute(&body, data); err != nil {
		return fmt.Errorf("render webhook body %q: %w", w.name, err)
	}

	// Expand env vars in URL
	url := os.ExpandEnv(w.url)

	req, err := http.NewRequestWithContext(ctx, w.method, url, &body)
	if err != nil {
		return fmt.Errorf("create webhook request %q: %w", w.name, err)
	}

	// Set default content type if not specified
	hasContentType := false
	for k, v := range w.headers {
		req.Header.Set(k, v)
		if strings.EqualFold(k, "content-type") {
			hasContentType = true
		}
	}
	if !hasContentType {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := w.client.Do(req) // #nosec G704 -- URL from local config
	if err != nil {
		return fmt.Errorf("webhook %q: %w", w.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook %q: HTTP %d", w.name, resp.StatusCode)
	}
	return nil
}
