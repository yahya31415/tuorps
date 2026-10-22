// Package core builds transactional emails as pure functions: validated
// messages, rendered text and HTML parts, and an RFC 5322 MIME document.
// Nothing here sends mail or reads the clock; the shell supplies the date,
// Message-ID and MIME boundary.
package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"maps"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	texttemplate "text/template"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxValueLen = 256

// ErrInvalidMessage reports a message that cannot be sent safely.
var ErrInvalidMessage = errors.New("email: invalid message")

// Kind is a transactional email type.
type Kind string

const (
	KindVerifyEmail     Kind = "verify_email"
	KindPasswordReset   Kind = "password_reset"
	KindInvitation      Kind = "invitation"
	KindPasswordChanged Kind = "password_changed"
	KindMFAEnabled      Kind = "mfa_enabled"
	KindMFADisabled     Kind = "mfa_disabled"
	KindAccountExists   Kind = "account_exists" // someone tried to sign up with this address

	KindConnectionExpiring    Kind = "connection_expiring"     // a connected account stops working soon
	KindConnectionNeedsReauth Kind = "connection_needs_reauth" // a connected account stopped working
	KindVersionSunset         Kind = "ops_version_sunset"      // to operators: a pinned API version sunsets soon
)

// requiredData lists the data keys each kind needs, exactly.
func requiredData(k Kind) ([]string, bool) {
	switch k {
	case KindVerifyEmail, KindPasswordReset, KindAccountExists:
		return []string{"link", "name"}, true
	case KindInvitation:
		return []string{"inviter", "link", "workspace"}, true
	case KindPasswordChanged, KindMFAEnabled, KindMFADisabled:
		return []string{"name"}, true
	case KindConnectionExpiring:
		return []string{"account", "days", "link", "name", "platform"}, true
	case KindConnectionNeedsReauth:
		return []string{"account", "link", "name", "platform"}, true
	case KindVersionSunset:
		return []string{"api", "days", "sunset", "version"}, true
	}
	return nil, false
}

// Message is a validated email to one recipient.
type Message struct {
	to     mail.Address
	kind   Kind
	locale string
	data   map[string]string
}

func (m Message) To() mail.Address        { return m.to }
func (m Message) Kind() Kind              { return m.kind }
func (m Message) Locale() string          { return m.locale }
func (m Message) Data() map[string]string { return maps.Clone(m.data) }

// wireMessage is the JSON form of a Message (for job payloads).
type wireMessage struct {
	To     mail.Address      `json:"to"`
	Kind   Kind              `json:"kind"`
	Locale string            `json:"locale"`
	Data   map[string]string `json:"data"`
}

// MarshalJSON encodes m for storage in a job.
func (m Message) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireMessage{To: m.to, Kind: m.kind, Locale: m.locale, Data: m.data})
}

// ParseMessage decodes MarshalJSON output and validates it again, so a
// stored payload can never bypass NewMessage.
func ParseMessage(b []byte) (Message, error) {
	var w wireMessage
	if err := json.Unmarshal(b, &w); err != nil {
		return Message{}, fmt.Errorf("%w: payload", ErrInvalidMessage)
	}
	return NewMessage(w.To, w.Kind, w.Locale, w.Data)
}

// NewMessage validates the recipient and the data for kind.
func NewMessage(to mail.Address, kind Kind, locale string, data map[string]string) (Message, error) {
	keys, ok := requiredData(kind)
	if !ok {
		return Message{}, fmt.Errorf("%w: unknown kind", ErrInvalidMessage)
	}
	if !safeHeaderText(to.Name) || !validAddress(to.Address) {
		return Message{}, fmt.Errorf("%w: recipient", ErrInvalidMessage)
	}
	got := slices.Sorted(maps.Keys(data))
	if !slices.Equal(got, keys) {
		return Message{}, fmt.Errorf("%w: %s needs exactly %v", ErrInvalidMessage, kind, keys)
	}
	for _, v := range data {
		if len(v) > maxValueLen || !safeHeaderText(v) {
			return Message{}, fmt.Errorf("%w: data value", ErrInvalidMessage)
		}
	}
	return Message{to: to, kind: kind, locale: locale, data: maps.Clone(data)}, nil
}

// Link builds an absolute link on origin. Parameters go in the URL
// fragment, which browsers never send to servers, logs or Referer headers.
func Link(origin *url.URL, path string, params map[string]string) string {
	v := url.Values{}
	for k, p := range params {
		v.Set(k, p)
	}
	return (&url.URL{Scheme: origin.Scheme, Host: origin.Host, Path: path}).String() + "#" + v.Encode()
}

// Rendered holds the subject and the two body parts.
type Rendered struct {
	Subject string
	Text    string
	HTML    string
}

// Render produces the subject and bodies for m in its locale (falling back
// to English).
func Render(m Message) (Rendered, error) {
	if link, ok := m.data["link"]; ok {
		u, err := url.Parse(link)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return Rendered{}, fmt.Errorf("%w: link must be an absolute http(s) URL", ErrInvalidMessage)
		}
	}
	t, ok := templatesFor(m.locale)[m.kind]
	if !ok {
		return Rendered{}, fmt.Errorf("%w: no template", ErrInvalidMessage)
	}
	subject, err := execText(t.subject, m.data)
	if err != nil {
		return Rendered{}, err
	}
	text, err := execText(t.text+textFooter, m.data)
	if err != nil {
		return Rendered{}, err
	}
	h, err := htmltemplate.New("h").Option("missingkey=error").Parse(htmlHeader + t.html + htmlFooter)
	if err != nil {
		return Rendered{}, fmt.Errorf("email: template: %w", err)
	}
	var html bytes.Buffer
	if err := h.Execute(&html, m.data); err != nil {
		return Rendered{}, fmt.Errorf("email: render: %w", err)
	}
	return Rendered{Subject: subject, Text: text, HTML: html.String()}, nil
}

func execText(src string, data map[string]string) (string, error) {
	t, err := texttemplate.New("t").Option("missingkey=error").Parse(src)
	if err != nil {
		return "", fmt.Errorf("email: template: %w", err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("email: render: %w", err)
	}
	return b.String(), nil
}

// BuildMIME renders m as a multipart/alternative RFC 5322 message with CRLF
// line endings and quoted-printable UTF-8 parts.
func BuildMIME(from mail.Address, m Message, r Rendered, messageID string, date time.Time, boundary string) ([]byte, error) {
	if !safeHeaderText(r.Subject) || !validMessageID(messageID) || !validBoundary(boundary) ||
		!safeHeaderText(from.Name) || !validAddress(from.Address) {
		return nil, fmt.Errorf("%w: headers", ErrInvalidMessage)
	}
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", formatAddress(from))
	header("To", formatAddress(m.to))
	header("Subject", mime.QEncoding.Encode("utf-8", r.Subject))
	header("Date", date.Format(time.RFC1123Z))
	header("Message-ID", messageID)
	header("MIME-Version", "1.0")
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ contentType, body string }{
		{"text/plain; charset=utf-8", r.Text},
		{"text/html; charset=utf-8", r.HTML},
	} {
		b.WriteString("--" + boundary + "\r\n")
		header("Content-Type", part.contentType)
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		qp := quotedprintable.NewWriter(&b)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

// safeHeaderText rejects control characters (CR/LF above all) and invalid UTF-8.
func safeHeaderText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

func validAddress(a string) bool {
	if a == "" || !safeHeaderText(a) {
		return false
	}
	parsed, err := mail.ParseAddress(a)
	return err == nil && parsed.Address == a
}

func validMessageID(id string) bool {
	return len(id) <= 255 && strings.HasPrefix(id, "<") && strings.HasSuffix(id, ">") && strings.Contains(id, "@") &&
		!strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r > '~' })
}

func validBoundary(b string) bool {
	if b == "" || len(b) > 70 {
		return false
	}
	return !strings.ContainsFunc(b, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	})
}

// formatAddress renders a for an address header. mail.Address.String
// Q-encodes non-ASCII names, and Q-encoding leaves characters such as '\'
// that are not valid in a display-name atom; B-encoding never does.
func formatAddress(a mail.Address) string {
	for _, r := range a.Name {
		if r >= utf8.RuneSelf || !unicode.IsPrint(r) {
			return mime.BEncoding.Encode("utf-8", a.Name) + " <" + a.Address + ">"
		}
	}
	return a.String()
}
