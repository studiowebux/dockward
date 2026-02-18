package notify

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPNotifier sends alerts via email.
type SMTPNotifier struct {
	host     string
	port     int
	from     string
	to       string
	username string
	password string
}

// NewSMTP creates an SMTP email notifier.
func NewSMTP(host string, port int, from, to, username, password string) *SMTPNotifier {
	return &SMTPNotifier{
		host:     host,
		port:     port,
		from:     from,
		to:       to,
		username: username,
		password: password,
	}
}

func (s *SMTPNotifier) Name() string { return "smtp" }

func (s *SMTPNotifier) Send(_ context.Context, alert Alert) error {
	subject := fmt.Sprintf("[watcher] [%s] %s: %s", alert.Level, alert.Event, alert.Service)

	var body strings.Builder
	body.WriteString(fmt.Sprintf("Service: %s\n", alert.Service))
	body.WriteString(fmt.Sprintf("Event: %s\n", alert.Event))
	body.WriteString(fmt.Sprintf("Container: %s\n", alert.Container))
	body.WriteString(fmt.Sprintf("Time: %s\n\n", alert.Timestamp.Format("2006-01-02 15:04:05 UTC")))
	body.WriteString(alert.Message)
	if alert.Reason != "" {
		body.WriteString(fmt.Sprintf("\n\nReason: %s", alert.Reason))
	}
	if alert.OldDigest != "" {
		body.WriteString(fmt.Sprintf("\nOld digest: %s", alert.OldDigest))
	}
	if alert.NewDigest != "" {
		body.WriteString(fmt.Sprintf("\nNew digest: %s", alert.NewDigest))
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		s.from, s.to, subject, body.String())

	addr := fmt.Sprintf("%s:%d", s.host, s.port)

	var auth smtp.Auth
	if s.username != "" {
		auth = smtp.PlainAuth("", s.username, s.password, s.host)
	}

	if err := smtp.SendMail(addr, auth, s.from, []string{s.to}, []byte(msg)); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	return nil
}
