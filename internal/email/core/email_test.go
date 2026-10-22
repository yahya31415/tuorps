package core_test

import (
	"errors"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/email/core"
)

func from() mail.Address { return mail.Address{Name: "Tuorps", Address: "no-reply@tuorps.in"} }

func origin() *url.URL { return &url.URL{Scheme: "https", Host: "app.tuorps.in"} }

func msg(t *testing.T, kind core.Kind, data map[string]string) core.Message {
	t.Helper()
	m, err := core.NewMessage(mail.Address{Name: "Priya Sharma", Address: "priya@example.in"}, kind, "en", data)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return m
}

func TestLinkPutsTokensInTheFragment(t *testing.T) {
	got := core.Link(origin(), "/verify-email", map[string]string{"token": "abc+/="})
	if got != "https://app.tuorps.in/verify-email#token=abc%2B%2F%3D" {
		t.Fatalf("Link = %q", got)
	}
	u, err := url.Parse(got)
	if err != nil || u.RawQuery != "" {
		t.Errorf("token must not be in the query string: %q", got)
	}
}

func TestRenderEveryKind(t *testing.T) {
	link := "https://app.tuorps.in/x#token=t0k3n"
	cases := []struct {
		kind core.Kind
		data map[string]string
		want []string
	}{
		{core.KindVerifyEmail, map[string]string{"name": "Priya", "link": link}, []string{"Priya", link, "Verify"}},
		{core.KindPasswordReset, map[string]string{"name": "Priya", "link": link}, []string{"Priya", link, "reset"}},
		{core.KindInvitation, map[string]string{"inviter": "Arjun", "workspace": "Kerala Spices", "link": link}, []string{"Arjun", "Kerala Spices", link}},
		{core.KindPasswordChanged, map[string]string{"name": "Priya"}, []string{"Priya", "password"}},
		{core.KindMFAEnabled, map[string]string{"name": "Priya"}, []string{"Priya", "two-factor"}},
		{core.KindMFADisabled, map[string]string{"name": "Priya"}, []string{"Priya", "two-factor"}},
		{core.KindAccountExists, map[string]string{"name": "Priya", "link": link}, []string{"Priya", link}},
		{core.KindConnectionExpiring, map[string]string{"name": "Priya", "account": "Kerala Spices", "platform": "LinkedIn", "days": "14", "link": link},
			[]string{"Priya", "Kerala Spices", "LinkedIn", "14 days", link}},
		{core.KindConnectionNeedsReauth, map[string]string{"name": "Priya", "account": "Kerala Spices", "platform": "Facebook", "link": link},
			[]string{"Priya", "Kerala Spices", "Facebook", "reconnect", link}},
		{core.KindVersionSunset, map[string]string{"api": "linkedin", "version": "202610", "sunset": "2027-10-15", "days": "60"},
			[]string{"linkedin", "202610", "2027-10-15", "60 days"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			r, err := core.Render(msg(t, tc.kind, tc.data))
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if r.Subject == "" || strings.ContainsAny(r.Subject, "\r\n") {
				t.Errorf("subject = %q", r.Subject)
			}
			for _, w := range tc.want {
				if !strings.Contains(strings.ToLower(r.Text), strings.ToLower(w)) || !strings.Contains(strings.ToLower(r.HTML), strings.ToLower(strings.ReplaceAll(w, "&", "&amp;"))) {
					t.Errorf("rendered %s missing %q:\ntext: %s\nhtml: %s", tc.kind, w, r.Text, r.HTML)
				}
			}
		})
	}
}

func TestRenderEscapesHTML(t *testing.T) {
	r, err := core.Render(msg(t, core.KindInvitation, map[string]string{"inviter": "<script>alert(1)</script>", "workspace": "A & B", "link": "https://app.tuorps.in/i#token=x"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.HTML, "<script>") || !strings.Contains(r.HTML, "&lt;script&gt;") || !strings.Contains(r.HTML, "A &amp; B") {
		t.Errorf("HTML not escaped:\n%s", r.HTML)
	}
}

func TestRenderRejectsUnsafeLinks(t *testing.T) {
	r, err := core.Render(msg(t, core.KindVerifyEmail, map[string]string{"name": "P", "link": "javascript:alert(1)"}))
	if err == nil && strings.Contains(r.HTML, "javascript:") {
		t.Fatalf("javascript: link rendered:\n%s", r.HTML)
	}
}

func TestNewMessageValidates(t *testing.T) {
	to := mail.Address{Name: "P", Address: "p@example.in"}
	cases := []struct {
		name string
		to   mail.Address
		kind core.Kind
		data map[string]string
	}{
		{"unknown kind", to, core.Kind("newsletter"), map[string]string{"name": "x"}},
		{"missing data", to, core.KindVerifyEmail, map[string]string{"name": "x"}},
		{"unexpected data", to, core.KindPasswordChanged, map[string]string{"name": "x", "extra": "y"}},
		{"bad address", mail.Address{Address: "not-an-address"}, core.KindPasswordChanged, map[string]string{"name": "x"}},
		{"newline in name", mail.Address{Name: "P\r\nBcc: evil@x.in", Address: "p@example.in"}, core.KindPasswordChanged, map[string]string{"name": "x"}},
		{"newline in address", mail.Address{Address: "p@example.in\r\nBcc: evil@x.in"}, core.KindPasswordChanged, map[string]string{"name": "x"}},
		{"overlong value", to, core.KindPasswordChanged, map[string]string{"name": strings.Repeat("x", 300)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := core.NewMessage(tc.to, tc.kind, "en", tc.data); !errors.Is(err, core.ErrInvalidMessage) {
				t.Fatalf("err = %v, want ErrInvalidMessage", err)
			}
		})
	}
}

func TestUnknownLocaleFallsBackToEnglish(t *testing.T) {
	m, err := core.NewMessage(mail.Address{Address: "p@example.in"}, core.KindPasswordChanged, "ml", map[string]string{"name": "P"})
	if err != nil {
		t.Fatal(err)
	}
	en, _ := core.Render(msg(t, core.KindPasswordChanged, map[string]string{"name": "P"}))
	ml, err := core.Render(m)
	if err != nil || ml.Subject != en.Subject {
		t.Fatalf("ml subject = %q, en = %q, %v", ml.Subject, en.Subject, err)
	}
}

func parse(t *testing.T, raw []byte) (*mail.Message, map[string]string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("not a valid RFC 5322 message: %v\n%s", err, raw)
	}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("Content-Type = %q", m.Header.Get("Content-Type"))
	}
	parts := map[string]string{}
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		p, err := mr.NextRawPart() // NextPart would decode quoted-printable itself
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if p.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
			t.Errorf("part encoding = %q", p.Header.Get("Content-Transfer-Encoding"))
		}
		body, err := io.ReadAll(quotedprintable.NewReader(p))
		if err != nil {
			t.Fatal(err)
		}
		parts[p.Header.Get("Content-Type")] = strings.ReplaceAll(string(body), "\r\n", "\n") // CRLF on the wire
	}
	return m, parts
}

func TestBuildMIME(t *testing.T) {
	m, err := core.NewMessage(mail.Address{Name: "प्रिया शर्मा", Address: "priya@example.in"}, core.KindVerifyEmail, "en",
		map[string]string{"name": "प्रिया", "link": "https://app.tuorps.in/verify-email#token=t0k3n"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := core.Render(m)
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	raw, err := core.BuildMIME(from(), m, r, "<0199c3f0@tuorps.in>", date, "b0undary")
	if err != nil {
		t.Fatalf("BuildMIME: %v", err)
	}
	parsed, parts := parse(t, raw)
	dec := new(mime.WordDecoder)
	subject, _ := dec.DecodeHeader(parsed.Header.Get("Subject"))
	to, err := parsed.Header.AddressList("To")
	if err != nil || len(to) != 1 || to[0].Name != "प्रिया शर्मा" || to[0].Address != "priya@example.in" {
		t.Errorf("To = %v, %v", to, err)
	}
	if f, _ := parsed.Header.AddressList("From"); len(f) != 1 || f[0].Address != "no-reply@tuorps.in" {
		t.Errorf("From = %v", f)
	}
	if subject != r.Subject || parsed.Header.Get("Message-Id") != "<0199c3f0@tuorps.in>" || parsed.Header.Get("Mime-Version") != "1.0" {
		t.Errorf("headers: subject=%q id=%q mime=%q", subject, parsed.Header.Get("Message-Id"), parsed.Header.Get("Mime-Version"))
	}
	if d, err := parsed.Header.Date(); err != nil || !d.Equal(date) {
		t.Errorf("Date = %v, %v", d, err)
	}
	if parts["text/plain; charset=utf-8"] != r.Text || parts["text/html; charset=utf-8"] != r.HTML {
		t.Errorf("parts = %v", parts)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line longer than RFC 5322 allows: %d", len(line))
		}
	}
}

func TestBuildMIMERejectsBadInputs(t *testing.T) {
	m := msg(t, core.KindPasswordChanged, map[string]string{"name": "P"})
	r, _ := core.Render(m)
	for name, args := range map[string]struct{ id, boundary string }{
		"newline in message id": {"<a@b>\r\nBcc: x@y", "b"},
		"bad boundary":          {"<a@b>", "has space\r\n"},
		"empty boundary":        {"<a@b>", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := core.BuildMIME(from(), m, r, args.id, time.Now(), args.boundary); !errors.Is(err, core.ErrInvalidMessage) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	evil := core.Rendered{Subject: "hi\r\nBcc: x@y", Text: "t", HTML: "h"}
	if _, err := core.BuildMIME(from(), m, evil, "<a@b>", time.Now(), "b"); !errors.Is(err, core.ErrInvalidMessage) {
		t.Fatalf("subject injection: err = %v", err)
	}
}

// Fuzz the whole pipeline with hostile names and data: either it is refused,
// or the result parses and carries exactly the intended recipient.
func FuzzMessagePipeline(f *testing.F) {
	f.Add("Priya", "Kerala Spices")
	f.Add("x\r\nBcc: evil@x.in", "y")
	f.Add("\"quoted\" <name>", "=?utf-8?q?enc?=")
	f.Fuzz(func(t *testing.T, name, workspace string) {
		m, err := core.NewMessage(mail.Address{Name: name, Address: "p@example.in"}, core.KindInvitation, "en",
			map[string]string{"inviter": name, "workspace": workspace, "link": "https://app.tuorps.in/i#token=x"})
		if err != nil {
			return
		}
		r, err := core.Render(m)
		if err != nil {
			return
		}
		raw, err := core.BuildMIME(from(), m, r, "<id@tuorps.in>", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "b0undary")
		if err != nil {
			return
		}
		parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatalf("unparseable message for name %q: %v", name, err)
		}
		if parsed.Header.Get("Bcc") != "" || parsed.Header.Get("Cc") != "" {
			t.Fatalf("header injection via %q", name)
		}
		to, err := parsed.Header.AddressList("To")
		if err != nil || len(to) != 1 || to[0].Address != "p@example.in" {
			t.Fatalf("recipient changed: %v %v", to, err)
		}
	})
}

func TestMessageJSONRoundTripRevalidates(t *testing.T) {
	m := msg(t, core.KindInvitation, map[string]string{"inviter": "Arjun", "workspace": "Kerala Spices", "link": "https://app.tuorps.in/i#token=x"})
	b, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	back, err := core.ParseMessage(b)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if back.To() != m.To() || back.Kind() != m.Kind() || back.Locale() != m.Locale() || !maps.Equal(back.Data(), m.Data()) {
		t.Errorf("round trip = %+v, want %+v", back, m)
	}
	d := back.Data()
	d["inviter"] = "mutated"
	if back.Data()["inviter"] != "Arjun" {
		t.Error("Data exposes internal state")
	}
	for name, raw := range map[string]string{
		"not json":      `{`,
		"unknown kind":  `{"to":{"Name":"","Address":"p@example.in"},"kind":"spam","locale":"en","data":{}}`,
		"injected name": `{"to":{"Name":"x\r\nBcc: e@x.in","Address":"p@example.in"},"kind":"password_changed","locale":"en","data":{"name":"x"}}`,
		"missing data":  `{"to":{"Name":"","Address":"p@example.in"},"kind":"verify_email","locale":"en","data":{"name":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := core.ParseMessage([]byte(raw)); !errors.Is(err, core.ErrInvalidMessage) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
