package mail

import (
	"context"
	"fmt"
	"net/smtp"
)

// SMTPConfig holds SMTP relay configuration.
type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

// SMTPSender sends emails via an SMTP relay.
type SMTPSender struct {
	cfg SMTPConfig
}

// NewSMTPSender creates a sender that connects to the configured SMTP relay.
func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	return &SMTPSender{cfg: cfg}
}

// Send delivers the message via SMTP. The template is rendered to a plain-text
// body (HTML templates are a Phase 2 concern).
func (s *SMTPSender) Send(_ context.Context, msg Message) error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)

	subject := fmt.Sprintf("[BCARS Portal] %s", msg.TemplateID)
	body := fmt.Sprintf("Template: %s\n", msg.TemplateID)
	for k, v := range msg.Payload {
		body += fmt.Sprintf("%s: %s\n", k, v)
	}

	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		s.cfg.From, msg.To, subject, body)

	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)
	}

	if err := smtp.SendMail(addr, auth, s.cfg.From, []string{msg.To}, []byte(raw)); err != nil {
		return fmt.Errorf("mail/smtp: send to %s: %w", msg.To, err)
	}
	return nil
}
