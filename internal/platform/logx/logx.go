// Package logx provides the process logger: JSON slog output wrapped in a
// handler that redacts secrets, tokens, phone numbers and email addresses
// (CLAUDE.md §3). Redaction works on attribute keys and on string content,
// including the message, nested groups, errors and LogValuers.
package logx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"regexp"
	"strings"
)

// Redacted replaces every sensitive value.
const Redacted = "[REDACTED]"

// New returns a JSON logger writing to w at level, with redaction applied.
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(NewRedactingHandler(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})))
}

// NewRedactingHandler wraps next so that sensitive data never reaches it.
func NewRedactingHandler(next slog.Handler) slog.Handler {
	return &handler{next: next, r: newRedactor()}
}

type handler struct {
	next slog.Handler
	r    *redactor
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.text(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.r.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.r.attr(a)
	}
	return &handler{next: h.next.WithAttrs(clean), r: h.r}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{next: h.next.WithGroup(name), r: h.r}
}

// redactor holds the compiled patterns; it is immutable after construction.
type redactor struct {
	patterns []*regexp.Regexp
	userinfo *regexp.Regexp
}

func newRedactor() *redactor {
	return &redactor{
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),           // email
			regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=\-]+`),              // auth header values
			regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`), // JWT
			regexp.MustCompile(`\bya29\.[A-Za-z0-9_\-]+`),                                    // Google access token
			regexp.MustCompile(`[A-Za-z0-9_\-]{40,}`),                                        // long opaque tokens (Meta EAA…, LinkedIn AQ…)
			regexp.MustCompile(`\bEAA[A-Za-z0-9]{20,}`),                                      // Meta tokens shorter than 40
			regexp.MustCompile(`\+[1-9](?:[\s\-]?\d){9,14}\b`),                               // E.164 phone, optional separators
			regexp.MustCompile(`\b(?:0|91)?[6-9]\d{4}[\s\-]?\d{5}\b`),                        // Indian mobile
		},
		userinfo: regexp.MustCompile(`(://[^/\s:@]*:)[^@\s/]+@`), // password in URL userinfo
	}
}

// text masks sensitive substrings of s. Strings that cannot match any
// pattern are returned unchanged without running the regexes.
func (r *redactor) text(s string) string {
	if !mayContainSensitive(s) {
		return s
	}
	if r.userinfo.MatchString(s) {
		s = r.userinfo.ReplaceAllString(s, "${1}"+Redacted+"@")
	}
	for _, p := range r.patterns {
		if p.MatchString(s) {
			s = p.ReplaceAllString(s, Redacted)
		}
	}
	return s
}

// mayContainSensitive is an allocation-free superset of the patterns: it is
// true whenever any pattern could match. It looks for '@', "://", ten digits
// in a phone-like run, a 20-character alphanumeric run (JWT, Meta, Google
// tokens), a 40-character token run, a Bearer/Basic prefix, or the "ya29."
// and "eyJ" prefixes of Google tokens and JWTs.
func mayContainSensitive(s string) bool {
	phoneDigits, alnumRun, tokenRun := 0, 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '@' || (c == ':' && strings.HasPrefix(s[i:], "://")) {
			return true
		}
		digit := '0' <= c && c <= '9'
		alnum := digit || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || c == '_'
		switch {
		case digit:
			if phoneDigits++; phoneDigits >= 10 {
				return true
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' || c == '-' || c == '+' || c == '(' || c == ')':
		default:
			phoneDigits = 0
		}
		if alnum {
			alnumRun++
		} else {
			alnumRun = 0
		}
		if alnum || c == '-' {
			tokenRun++
		} else {
			tokenRun = 0
		}
		if alnumRun >= 20 || tokenRun >= 40 {
			return true
		}
	}
	return containsFold(s, "bearer") || containsFold(s, "basic") ||
		strings.Contains(s, "ya29.") || strings.Contains(s, "eyJ")
}

// containsFold reports whether s contains the ASCII substring sub, ignoring case.
func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func (r *redactor) attr(a slog.Attr) slog.Attr {
	if sensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, r.text(v.String()))
	case slog.KindGroup:
		group := v.Group()
		clean := make([]slog.Attr, len(group))
		for i, g := range group {
			clean[i] = r.attr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	case slog.KindAny:
		// Errors, Stringers and arbitrary values are rendered to text so their
		// content can be inspected; structure is traded for safety.
		return slog.String(a.Key, r.text(fmt.Sprintf("%+v", v.Any())))
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}

// sensitiveKey reports whether an attribute key names sensitive data.
// Long markers match anywhere in the key once lower-cased with separators
// removed; short ones must be a whole word to avoid false positives such as
// "footprint". Allocation-free for keys up to 64 bytes.
func sensitiveKey(key string) bool {
	var buf [64]byte
	if len(key) > len(buf) {
		return sensitiveNorm([]byte(strings.ToLower(key)), key)
	}
	n := 0
	for i := 0; i < len(key); i++ {
		switch c := key[i]; {
		case 'A' <= c && c <= 'Z':
			buf[n] = c + ('a' - 'A')
			n++
		case ('a' <= c && c <= 'z') || ('0' <= c && c <= '9'):
			buf[n] = c
			n++
		}
	}
	return sensitiveNorm(buf[:n], key)
}

func sensitiveNorm(norm []byte, key string) bool {
	for _, m := range [...]string{"password", "passwd", "secret", "token", "authorization", "cookie", "apikey", "phone", "mobile", "msisdn", "email", "verifier", "signedrequest"} {
		if bytes.Contains(norm, []byte(m)) {
			return true
		}
	}
	// OAuth codes and state, matched as whole keys so that diagnostic keys
	// such as status_code or connection_status stay readable.
	for _, whole := range [...]string{"code", "authcode", "state", "oauthstate"} {
		if string(norm) == whole {
			return true
		}
	}
	for w := range words(key) {
		for _, short := range [...]string{"otp", "totp", "pwd", "pin"} {
			if strings.EqualFold(w, short) {
				return true
			}
		}
	}
	return false
}

// words yields the words of key, split on non-alphanumerics and on
// lower-to-upper case changes ("refreshToken" -> "refresh", "Token").
func words(key string) iter.Seq[string] {
	return func(yield func(string) bool) {
		start := -1
		for i := 0; i <= len(key); i++ {
			var c byte
			if i < len(key) {
				c = key[i]
			}
			lower, upper, digit := 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9'
			alnum := lower || upper || digit
			camel := upper && i > 0 && 'a' <= key[i-1] && key[i-1] <= 'z'
			if start >= 0 && (!alnum || camel) {
				if !yield(key[start:i]) {
					return
				}
				start = -1
			}
			if alnum && start < 0 {
				start = i
			}
		}
	}
}
