package mail

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilelogSender(t *testing.T) {
	dir := t.TempDir()
	sender := NewFilelogSender(dir)

	msg := Message{
		To:         "officer@bcars.org",
		TemplateID: "password_recovery",
		Payload:    map[string]string{"token": "abc123", "url": "https://portal.bcars.org/recover?t=abc123"},
	}

	require.NoError(t, sender.Send(context.Background(), msg))

	entries, err := sender.ReadAll()
	require.NoError(t, err)
	require.Len(t, entries, 1)

	assert.Equal(t, "officer@bcars.org", entries[0].Message.To)
	assert.Equal(t, "password_recovery", entries[0].Message.TemplateID)
	assert.Equal(t, "abc123", entries[0].Message.Payload["token"])
	assert.NotEmpty(t, entries[0].SentAt)
}

func TestFilelogMultipleMessages(t *testing.T) {
	dir := t.TempDir()
	sender := NewFilelogSender(dir)

	for i := 0; i < 3; i++ {
		require.NoError(t, sender.Send(context.Background(), Message{
			To:         fmt.Sprintf("user%d@test.com", i),
			TemplateID: "invitation",
			Payload:    map[string]string{"name": fmt.Sprintf("User %d", i)},
		}))
	}

	entries, err := sender.ReadAll()
	require.NoError(t, err)
	assert.Len(t, entries, 3)
}

// TestSMTPSenderWithStub verifies the SMTP sender against a minimal stub
// server that accepts the SMTP handshake and captures the message.
func TestSMTPSenderWithStub(t *testing.T) {
	// Start a stub SMTP server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	received := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Minimal SMTP conversation.
		fmt.Fprintf(conn, "220 stub SMTP\r\n")
		scanner := bufio.NewScanner(conn)
		var dataMode bool
		var body strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if dataMode {
				if line == "." {
					dataMode = false
					fmt.Fprintf(conn, "250 OK\r\n")
					received <- body.String()
					continue
				}
				body.WriteString(line + "\n")
				continue
			}
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				fmt.Fprintf(conn, "250 Hello\r\n")
			case strings.HasPrefix(upper, "MAIL FROM:"):
				fmt.Fprintf(conn, "250 OK\r\n")
			case strings.HasPrefix(upper, "RCPT TO:"):
				fmt.Fprintf(conn, "250 OK\r\n")
			case upper == "DATA":
				fmt.Fprintf(conn, "354 Go ahead\r\n")
				dataMode = true
			case upper == "QUIT":
				fmt.Fprintf(conn, "221 Bye\r\n")
				return
			default:
				fmt.Fprintf(conn, "250 OK\r\n")
			}
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	sender := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1",
		Port: addr.Port,
		From: "portal@bcars.org",
	})

	msg := Message{
		To:         "admin@bcars.org",
		TemplateID: "test_template",
		Payload:    map[string]string{"key": "value"},
	}

	require.NoError(t, sender.Send(context.Background(), msg))

	body := <-received
	assert.Contains(t, body, "test_template")
	assert.Contains(t, body, "admin@bcars.org")
}
