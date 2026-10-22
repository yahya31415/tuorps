package logx_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"testing"
	"testing/quick"

	"github.com/yahya31415/tuorps/internal/platform/logx"
)

func newLogger(buf *bytes.Buffer) *slog.Logger { return logx.New(buf, slog.LevelDebug) }

func TestRedactsSensitiveKeysWhateverTheirValue(t *testing.T) {
	keys := []string{
		"password", "Password", "db_password", "secret", "client_secret", "token",
		"access_token", "refreshToken", "authorization", "Authorization", "cookie",
		"set-cookie", "api_key", "apikey", "phone", "phone_number", "mobile", "msisdn",
		"email", "user_email", "otp", "totp_code",
	}
	for _, k := range keys {
		t.Run(k, func(t *testing.T) {
			var buf bytes.Buffer
			newLogger(&buf).Info("x", k, "visible-value-123", k+"_n", 4242424)
			out := buf.String()
			if strings.Contains(out, "visible-value-123") || strings.Contains(out, "4242424") {
				t.Fatalf("value under %q leaked: %s", k, out)
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Fatalf("no redaction marker: %s", out)
			}
		})
	}
}

// OAuth authorization codes, state values and verifiers are bearer-like
// secrets for a few minutes; Meta and Instagram post a signed_request.
// They are redacted by key, but only as whole keys (or the unambiguous
// "verifier"/"signed_request"), so diagnostic keys such as status_code stay.
func TestRedactsOAuthKeys(t *testing.T) {
	for _, k := range []string{"code", "Code", "auth_code", "state", "oauth_state", "code_verifier", "codeVerifier", "signed_request", "signedRequest"} {
		t.Run(k, func(t *testing.T) {
			var buf bytes.Buffer
			newLogger(&buf).Info("x", k, "visible-value-123")
			if out := buf.String(); strings.Contains(out, "visible-value-123") || !strings.Contains(out, "[REDACTED]") {
				t.Fatalf("value under %q not redacted: %s", k, out)
			}
		})
	}
	for _, k := range []string{"status_code", "error_code", "http_status", "connection_status", "statement"} {
		t.Run("keeps "+k, func(t *testing.T) {
			var buf bytes.Buffer
			newLogger(&buf).Info("x", k, "visible-value-123")
			if out := buf.String(); !strings.Contains(out, "visible-value-123") {
				t.Fatalf("value under %q over-redacted: %s", k, out)
			}
		})
	}
}

func TestKeepsOrdinaryAttributes(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("request done", "status", 200, "path", "/v1/posts", "request_id", "0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f", "duration_ms", 12)
	out := buf.String()
	for _, want := range []string{`"msg":"request done"`, `"status":200`, `"path":"/v1/posts"`, `"request_id":"0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f"`, `"duration_ms":12`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "[REDACTED]") {
		t.Errorf("over-redacted: %s", out)
	}
}

func TestRedactsSensitivePatternsInValuesAndMessages(t *testing.T) {
	cases := []struct{ name, secret string }{
		{"email", "priya.sharma+test@example.co.in"},
		{"e164 phone", "+919876543210"},
		{"spaced e164 phone", "+91 98765 43210"},
		{"indian mobile", "9876543210"},
		{"indian mobile with dash", "98765-43210"},
		{"bearer token", "Bearer abc.def-ghi_jkl"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.c2lnbmF0dXJl"},
		{"meta token", "EAAGm0PX4ZCpsBAKZCZBqZB9ZCZB7ZCZBZB0ZCZBZB"},
		{"google token", "ya29.a0AfH6SMBx-very-long-token-value"},
		{"long opaque token", "AQXdSP_W41_UPs5ioT_t8HESyODB4FqbkJ8LrV_5mff4gPODzOYR"},
		{"url password", "postgres://app:hunter2@db:5432/tuorps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			newLogger(&buf).Info("contact "+tc.secret+" failed", "detail", "got "+tc.secret+" from webhook")
			leak := tc.secret
			if tc.name == "url password" {
				leak = "hunter2"
			}
			if tc.name == "bearer token" {
				leak = "abc.def-ghi_jkl"
			}
			if strings.Contains(buf.String(), leak) {
				t.Fatalf("leaked %q: %s", leak, buf.String())
			}
		})
	}
}

func TestDoesNotRedactDatesOrShortNumbers(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("scheduled for 2026-10-19 12:00", "count", "1234567", "post_id", "123")
	out := buf.String()
	for _, want := range []string{"2026-10-19 12:00", `"count":"1234567"`, `"post_id":"123"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestRedactsInsideGroupsAndPreboundAttrs(t *testing.T) {
	var buf bytes.Buffer
	l := newLogger(&buf).With("email", "a@b.co", "tenant", "w1").WithGroup("req")
	l.Info("x", slog.Group("user", "phone", "+919876543210", "note", "mail me at z@y.io"), "authorization", "Basic Zm9vOmJhcg==")
	out := buf.String()
	for _, leak := range []string{"a@b.co", "+919876543210", "z@y.io", "Zm9vOmJhcg=="} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, `"tenant":"w1"`) || !strings.Contains(out, `"req":{`) {
		t.Errorf("structure lost: %s", out)
	}
}

type userInfo struct{ Email string }

func (u userInfo) LogValue() slog.Value { return slog.StringValue("user " + u.Email) }

func TestRedactsErrorsStringersAndLogValuers(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Error("boom",
		"err", fmt.Errorf("send to %s: %w", "+919876543210", errors.New("rejected")),
		"who", userInfo{Email: "lv@example.com"},
		"raw", struct{ Mail string }{"st@example.com"},
	)
	out := buf.String()
	for _, leak := range []string{"+919876543210", "lv@example.com", "st@example.com"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "rejected") {
		t.Errorf("error text lost: %s", out)
	}
}

func TestEnabledDelegatesToLevel(t *testing.T) {
	l := logx.New(io.Discard, slog.LevelWarn)
	if l.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info enabled at warn level")
	}
	if !l.Enabled(context.Background(), slog.LevelError) {
		t.Error("error disabled at warn level")
	}
}

// Property: a generated email, phone or token placed anywhere in a log
// record never reaches the output.
func TestPropertySensitiveValuesNeverReachOutput(t *testing.T) {
	gen := func(r *rand.Rand) string {
		const letters = "abcdefghijklmnopqrstuvwxyz"
		word := func(n int) string {
			b := make([]byte, n)
			for i := range b {
				b[i] = letters[r.IntN(len(letters))]
			}
			return string(b)
		}
		digits := func(n int) string {
			b := make([]byte, n)
			for i := range b {
				b[i] = byte('0' + r.IntN(10))
			}
			return string(b)
		}
		switch r.IntN(4) {
		case 0:
			return word(3+r.IntN(8)) + "@" + word(3+r.IntN(8)) + ".com"
		case 1:
			return "+91" + string(byte('6'+r.IntN(4))) + digits(9)
		case 2:
			return string(byte('6'+r.IntN(4))) + digits(9)
		default:
			return "EAA" + word(30)
		}
	}
	prop := func(seed uint64, prefix, suffix string, where uint8) bool {
		r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		secret := gen(r)
		text := strings.ToValidUTF8(prefix, "") + " " + secret + " " + strings.ToValidUTF8(suffix, "")
		var buf bytes.Buffer
		l := newLogger(&buf)
		switch where % 4 {
		case 0:
			l.Info(text)
		case 1:
			l.Info("m", "note", text)
		case 2:
			l.Info("m", slog.Group("g", slog.Group("h", "note", text)))
		default:
			l.With("note", text).Info("m")
		}
		return !strings.Contains(buf.String(), secret)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

func BenchmarkHandlerOrdinaryRecord(b *testing.B) {
	l := logx.New(io.Discard, slog.LevelInfo)
	b.ReportAllocs()
	for b.Loop() {
		l.Info("request done", "status", 200, "path", "/v1/inbox/conversations", "request_id", "0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f", "duration_ms", 12)
	}
}

func BenchmarkHandlerSensitiveRecord(b *testing.B) {
	l := logx.New(io.Discard, slog.LevelInfo)
	b.ReportAllocs()
	for b.Loop() {
		l.Info("webhook from +919876543210", "email", "a@b.co", "note", "reply to priya@example.com")
	}
}

// Performance budget: redaction of an ordinary access-log record may add at
// most a handful of allocations on top of the plain JSON handler.
func TestOrdinaryRecordAllocationBudget(t *testing.T) {
	logOrdinary := func(l *slog.Logger) {
		l.Info("request done", "status", 200, "path", "/v1/inbox/conversations", "request_id", "0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f", "duration_ms", 12)
	}
	plain := slog.New(slog.NewJSONHandler(io.Discard, nil))
	redacting := logx.New(io.Discard, slog.LevelInfo)
	base := testing.AllocsPerRun(200, func() { logOrdinary(plain) })
	got := testing.AllocsPerRun(200, func() { logOrdinary(redacting) })
	if budget := base + 4; got > budget {
		t.Fatalf("redacting handler: %.0f allocs/record, budget %.0f (plain JSON handler: %.0f)", got, budget, base)
	}
}
