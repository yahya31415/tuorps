package config

import (
	"errors"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

var (
	errPort         = errors.New("invalid value: want a TCP port 1-65535")
	errSMTPTLS      = errors.New("invalid value: want starttls, implicit or none")
	errSMTPPlain    = errors.New("invalid value: plaintext SMTP is only allowed in dev")
	errMailFrom     = errors.New("invalid value: want an address such as Tuorps <no-reply@example.in>")
	errSMTPUsername = errors.New("required when SMTP_PASSWORD is set")
)

// SMTPTLS is the transport security mode for outgoing mail.
type SMTPTLS string

const (
	SMTPStartTLS    SMTPTLS = "starttls" // port 587: upgrade, then authenticate
	SMTPImplicitTLS SMTPTLS = "implicit" // port 465: TLS from the first byte
	SMTPNoTLS       SMTPTLS = "none"     // dev only (Mailpit)
)

// SMTP configures outgoing email (the worker sends; the api only enqueues).
type SMTP struct {
	Host     string
	Port     int
	TLS      SMTPTLS
	Username string
	Password Secret
	From     mail.Address
	Timeout  time.Duration
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, errPort
	}
	return n, nil
}

func parseSMTPTLS(s string) (SMTPTLS, error) {
	switch m := SMTPTLS(strings.ToLower(s)); m {
	case SMTPStartTLS, SMTPImplicitTLS, SMTPNoTLS:
		return m, nil
	}
	return "", errSMTPTLS
}

func parseMailFrom(s string) (mail.Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return mail.Address{}, errMailFrom
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return mail.Address{}, errMailFrom
	}
	return *a, nil
}

// LoadSMTP reads the SMTP settings, reporting every problem at once.
func LoadSMTP(lookup Lookup, readFile ReadFile, env Env) (SMTP, error) {
	r := &reader{lookup: lookup, readFile: readFile}
	host, ok := r.raw("SMTP_HOST")
	if !ok {
		r.fail("SMTP_HOST", ErrMissing)
	}
	from, ok := r.raw("MAIL_FROM")
	var addr mail.Address
	if !ok {
		r.fail("MAIL_FROM", ErrMissing)
	} else if a, err := parseMailFrom(from); err != nil {
		r.fail("MAIL_FROM", err)
	} else {
		addr = a
	}
	s := SMTP{
		Host:     host,
		Port:     opt(r, "SMTP_PORT", 587, parsePort),
		TLS:      opt(r, "SMTP_TLS", SMTPStartTLS, parseSMTPTLS),
		Username: opt(r, "SMTP_USERNAME", "", func(v string) (string, error) { return v, nil }),
		From:     addr,
		Timeout:  opt(r, "SMTP_TIMEOUT", 10*time.Second, parseDuration),
	}
	if _, direct := r.raw("SMTP_PASSWORD"); direct {
		s.Password = r.secret("SMTP_PASSWORD")
	} else if _, viaFile := r.raw("SMTP_PASSWORD_FILE"); viaFile {
		s.Password = r.secret("SMTP_PASSWORD")
	}
	if s.Password.Reveal() != "" && s.Username == "" {
		r.fail("SMTP_USERNAME", errSMTPUsername)
	}
	if s.TLS == SMTPNoTLS && env != EnvDev {
		r.fail("SMTP_TLS", errSMTPPlain)
	}
	if len(r.errs) > 0 {
		return SMTP{}, errors.Join(r.errs...)
	}
	return s, nil
}
