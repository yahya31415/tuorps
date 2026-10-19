// Package adapters delivers email over SMTP (provider-agnostic: Amazon SES
// SMTP in ap-south-1, ZeptoMail, Postmark; Mailpit in development).
package adapters

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

// ErrInvalidEnvelope reports an envelope address that is malformed or
// carries line breaks (SMTP command injection).
var ErrInvalidEnvelope = errors.New("smtp: invalid envelope address")

// Envelope is the SMTP envelope: MAIL FROM and RCPT TO.
type Envelope struct {
	From, To string
}

// SMTP sends messages through one SMTP server.
type SMTP struct {
	cfg config.SMTP
	tls *tls.Config
}

// NewSMTP returns a sender. tlsConfig overrides trust settings (tests pin a
// self-signed root); nil means system roots. The certificate is verified
// against tlsConfig.ServerName if set, otherwise cfg.Host; the minimum
// version is always TLS 1.2.
func NewSMTP(cfg config.SMTP, tlsConfig *tls.Config) SMTP {
	t := &tls.Config{MinVersion: tls.VersionTLS12}
	if tlsConfig != nil {
		t = tlsConfig.Clone()
	}
	if t.ServerName == "" {
		t.ServerName = cfg.Host
	}
	if t.MinVersion < tls.VersionTLS12 {
		t.MinVersion = tls.VersionTLS12
	}
	return SMTP{cfg: cfg, tls: t}
}

// Send delivers msg (a complete RFC 5322 message) to env.To. Every network
// operation is bounded by cfg.Timeout and by ctx.
func (s SMTP) Send(ctx context.Context, env Envelope, msg []byte) error {
	if !envelopeAddress(env.From) || !envelopeAddress(env.To) {
		return ErrInvalidEnvelope
	}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	dialer := &net.Dialer{Timeout: s.cfg.Timeout}
	var conn net.Conn
	var err error
	if s.cfg.TLS == config.SMTPImplicitTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: s.tls}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp: dial: %w", err)
	}
	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: greeting: %w", err)
	}
	defer func() { _ = c.Close() }()
	if s.cfg.TLS == config.SMTPStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server does not offer STARTTLS; refusing to send in plaintext")
		}
		if err := c.StartTLS(s.tls); err != nil {
			return fmt.Errorf("smtp: STARTTLS: %w", err)
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password.Reveal(), s.cfg.Host)); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}
	if err := c.Mail(env.From); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(env.To); err != nil {
		return fmt.Errorf("smtp: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: end of data: %w", err)
	}
	return c.Quit()
}

func envelopeAddress(a string) bool {
	if a == "" || strings.ContainsAny(a, "\r\n<>") {
		return false
	}
	p, err := mail.ParseAddress(a)
	return err == nil && p.Address == a
}
