// Package mail provides the Sender interface and implementations for sending
// templated emails. The filelog implementation writes messages to disk for
// dev/test use; the SMTP implementation sends via a relay.
package mail

import "context"

// Message represents a templated email to be sent.
type Message struct {
	To         string            `json:"to"`
	TemplateID string            `json:"template_id"`
	Payload    map[string]string `json:"payload"`
}

// Sender is the interface for sending emails. Implementations must be safe
// for concurrent use.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}
