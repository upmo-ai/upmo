// Package mail sends the one-time sign-in links by email: an invite to join
// and a link to recover access. It is not a general notification mailer.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	netmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/tlscheck"
)

// Sender sends one link email.
type Sender interface {
	SendInvite(ctx context.Context, to, link string) error
	SendRecover(ctx context.Context, to, link string) error
}

// Noop is the Sender when SMTP is unset. It reports success and sends nothing.
type Noop struct{}

// SendInvite does nothing.
func (Noop) SendInvite(context.Context, string, string) error { return nil }

// SendRecover does nothing.
func (Noop) SendRecover(context.Context, string, string) error { return nil }

// Timeout bounds one send when the caller's context has no earlier deadline.
const Timeout = 10 * time.Second

// ErrAddress is a recipient or sender that is not one email address.
var ErrAddress = errors.New("mail: not an email address")

// SMTP sends through one relay, upgrading with STARTTLS when the relay offers
// it. A relay that is neither loopback nor one DNS label is refused unless it
// offers STARTTLS. net/smtp refuses to send a password without TLS unless the
// relay is on localhost.
type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

// SendInvite sends the invite link to the invited person.
func (s SMTP) SendInvite(ctx context.Context, to, link string) error {
	return s.send(ctx, to, "You are invited to Upmo",
		"You have been invited to Upmo.\n\nOpen this link to set up how you log in:\n\n"+link+
			"\n\nThe link works once. If you did not expect this email, you can ignore it.\n")
}

// SendRecover sends the recover link to the person.
func (s SMTP) SendRecover(ctx context.Context, to, link string) error {
	return s.send(ctx, to, "Log in to Upmo",
		"Open this link to log in to Upmo:\n\n"+link+
			"\n\nThe link works once and expires soon. If you did not ask for it, you can ignore this email.\n")
}

func (s SMTP) send(ctx context.Context, to, subject, body string) error {
	rcpt, err := parse(to)
	if err != nil {
		return err
	}
	from, err := parse(s.From)
	if err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, Timeout)
		defer cancel()
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return fmt.Errorf("mail: dial: %w", err)
	}
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: %w", err)
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: greeting: %w", err)
	}
	defer func() { _ = client.Close() }()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	} else if !tlscheck.ExemptHost(s.Host) {
		return fmt.Errorf("mail: the relay must offer STARTTLS unless its host is loopback or one DNS label")
	}
	if s.User != "" {
		if err := client.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("mail: from: %w", err)
	}
	if err := client.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("mail: rcpt: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := w.Write(message(from, rcpt, subject, body, time.Now())); err != nil {
		_ = w.Close()
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("mail: quit: %w", err)
	}
	return nil
}

func message(from, to *netmail.Address, subject, body string, now time.Time) []byte {
	var b strings.Builder
	b.WriteString("From: " + from.String() + "\r\n")
	b.WriteString("To: " + to.String() + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// parse reads one address, refusing anything that could break a header or an
// SMTP command.
func parse(s string) (*netmail.Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, ErrAddress
	}
	parsed, err := netmail.ParseAddress(s)
	if err != nil || strings.ContainsAny(parsed.Address, "\r\n<>") {
		return nil, ErrAddress
	}
	return parsed, nil
}
