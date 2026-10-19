package authhttp_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	authhttp "github.com/yahya31415/tuorps/internal/auth/http"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
)

func TestSignupVerifyLogin(t *testing.T) {
	e := newEnv(t)

	t.Run("signup creates an unverified account, its workspace and first brand, and emails a link", func(t *testing.T) {
		c := e.client("198.51.100.1")
		r := c.post("/v1/auth/signup", map[string]string{"email": "Priya@Example.in", "password": "correct horse battery staple", "name": "Priya", "workspace_name": "Kerala Spices"})
		if r.Status != http.StatusAccepted {
			t.Fatalf("signup = %d %s", r.Status, r.Body)
		}
		m := e.lastEmailTo("Priya@Example.in", emailcore.KindVerifyEmail)
		if !strings.HasPrefix(m.Data()["link"], "https://app.tuorps.test/verify-email#token=") {
			t.Errorf("link = %q", m.Data()["link"])
		}
		if e.count(`SELECT count(*) FROM tuorps.users WHERE email_normalized = 'priya@example.in' AND email_verified_at IS NULL`) != 1 {
			t.Error("user not created unverified")
		}
		if e.count(`SELECT count(*) FROM tuorps.workspace_members m JOIN tuorps.users u ON u.id = m.user_id WHERE u.email_normalized = 'priya@example.in' AND m.is_owner`) != 1 {
			t.Error("owner membership missing")
		}
		if e.count(`SELECT count(*) FROM tuorps.brands b JOIN tuorps.workspaces w ON w.id = b.workspace_id WHERE w.name = 'Kerala Spices'`) != 1 {
			t.Error("first brand missing")
		}
		if e.count(`SELECT count(*) FROM tuorps.audit_log WHERE action = 'auth.signup'`) != 1 {
			t.Error("signup not audited")
		}
	})

	t.Run("signing up again with the same address reveals nothing and emails the owner", func(t *testing.T) {
		c := e.client("198.51.100.2")
		r := c.post("/v1/auth/signup", map[string]string{"email": "PRIYA@example.in", "password": "another long passphrase", "name": "Impostor", "workspace_name": "Fake"})
		if r.Status != http.StatusAccepted {
			t.Fatalf("duplicate signup = %d %s", r.Status, r.Body)
		}
		if e.count(`SELECT count(*) FROM tuorps.users WHERE email_normalized = 'priya@example.in'`) != 1 {
			t.Error("a second account was created")
		}
		if e.countEmails("Priya@Example.in", emailcore.KindAccountExists) != 1 {
			t.Error("owner not told about the attempt")
		}
		if e.count(`SELECT count(*) FROM tuorps.workspaces WHERE name = 'Fake'`) != 0 {
			t.Error("impostor workspace created")
		}
	})

	t.Run("signup validation", func(t *testing.T) {
		c := e.client("198.51.100.3")
		cases := []struct {
			name string
			body any
			want int
		}{
			{"weak password", map[string]string{"email": "a@example.in", "password": "password1234", "name": "A", "workspace_name": "W"}, http.StatusUnprocessableEntity},
			{"bad email", map[string]string{"email": "not-an-email", "password": "correct horse battery staple", "name": "A", "workspace_name": "W"}, http.StatusUnprocessableEntity},
			{"missing workspace", map[string]string{"email": "a@example.in", "password": "correct horse battery staple", "name": "A"}, http.StatusUnprocessableEntity},
			{"unknown field", map[string]any{"email": "a@example.in", "password": "correct horse battery staple", "name": "A", "workspace_name": "W", "is_admin": true}, http.StatusBadRequest},
		}
		for _, tc := range cases {
			if r := c.post("/v1/auth/signup", tc.body); r.Status != tc.want || r.errorCode(t) == "" {
				t.Errorf("%s: %d %s", tc.name, r.Status, r.Body)
			}
		}
	})

	t.Run("verify email: bad token, good token, reuse", func(t *testing.T) {
		c := e.client("198.51.100.4")
		if r := c.post("/v1/auth/verify-email", map[string]string{"token": strings.Repeat("A", 43)}); r.Status != http.StatusBadRequest || r.errorCode(t) != "invalid_token" {
			t.Errorf("unknown token: %d %s", r.Status, r.Body)
		}
		u := e.client("198.51.100.15")
		u.post("/v1/auth/signup", map[string]string{"email": "unverified@example.in", "password": "correct horse battery staple", "name": "U", "workspace_name": "U Co"})
		if r := u.post("/v1/auth/login", map[string]string{"email": "unverified@example.in", "password": "correct horse battery staple"}); r.Status != http.StatusForbidden || r.errorCode(t) != "email_not_verified" {
			t.Errorf("login before verification: %d %s", r.Status, r.Body)
		}
		tok := tokenFrom(t, e.lastEmailTo("Priya@Example.in", emailcore.KindVerifyEmail))
		if r := c.post("/v1/auth/verify-email", map[string]string{"token": tok}); r.Status != http.StatusNoContent {
			t.Fatalf("verify = %d %s", r.Status, r.Body)
		}
		if r := c.post("/v1/auth/verify-email", map[string]string{"token": tok}); r.Status != http.StatusBadRequest {
			t.Errorf("reused token: %d", r.Status)
		}
	})

	t.Run("wrong password and unknown address are indistinguishable", func(t *testing.T) {
		c := e.client("198.51.100.5")
		wrong := c.post("/v1/auth/login", map[string]string{"email": "priya@example.in", "password": "wrong password here"})
		unknown := c.post("/v1/auth/login", map[string]string{"email": "nobody@example.in", "password": "wrong password here"})
		if wrong.Status != http.StatusUnauthorized || unknown.Status != http.StatusUnauthorized {
			t.Fatalf("statuses %d / %d", wrong.Status, unknown.Status)
		}
		if wrong.errorCode(t) != "invalid_credentials" || unknown.errorCode(t) != "invalid_credentials" {
			t.Errorf("codes %s / %s", wrong.Body, unknown.Body)
		}
		strip := func(b []byte) string { s := string(b); return s[:strings.Index(s, `"request_id"`)] }
		if strip(wrong.Body) != strip(unknown.Body) {
			t.Errorf("bodies differ:\n%s\n%s", wrong.Body, unknown.Body)
		}
	})

	t.Run("login sets a hardened session cookie and /v1/me works", func(t *testing.T) {
		c := e.client("198.51.100.6")
		r := c.post("/v1/auth/login", map[string]string{"email": "priya@example.in", "password": "correct horse battery staple"})
		if r.Status != http.StatusOK {
			t.Fatalf("login = %d %s", r.Status, r.Body)
		}
		cookie := r.Header.Get("Set-Cookie")
		for _, attr := range []string{"__Host-tuorps_session=", "Secure", "HttpOnly", "SameSite=Lax", "Path=/", "Max-Age="} {
			if !strings.Contains(cookie, attr) {
				t.Errorf("cookie %q missing %s", cookie, attr)
			}
		}
		me := c.get("/v1/me")
		var body struct {
			Email         string `json:"email"`
			Name          string `json:"name"`
			EmailVerified bool   `json:"email_verified"`
			MFAEnabled    bool   `json:"mfa_enabled"`
		}
		me.json(t, &body)
		if me.Status != http.StatusOK || body.Email != "Priya@Example.in" || body.Name != "Priya" || !body.EmailVerified || body.MFAEnabled {
			t.Errorf("/v1/me = %d %s", me.Status, me.Body)
		}
		if e.count(`SELECT count(*) FROM tuorps.audit_log WHERE action = 'auth.login'`) < 1 {
			t.Error("login not audited")
		}
	})

	t.Run("logging in again rotates the session", func(t *testing.T) {
		c := e.client("198.51.100.7")
		c.post("/v1/auth/login", map[string]string{"email": "priya@example.in", "password": "correct horse battery staple"})
		first := c.http.Jar.Cookies(urlOf(t, e))[0].Value
		c.post("/v1/auth/login", map[string]string{"email": "priya@example.in", "password": "correct horse battery staple"})
		second := c.http.Jar.Cookies(urlOf(t, e))[0].Value
		if first == second {
			t.Fatal("session token not rotated")
		}
		stale := e.client("198.51.100.8")
		stale.http.Jar.SetCookies(urlOf(t, e), []*http.Cookie{{Name: "__Host-tuorps_session", Value: first, Secure: true, Path: "/"}})
		if r := stale.get("/v1/me"); r.Status != http.StatusUnauthorized {
			t.Errorf("old session still valid: %d", r.Status)
		}
	})

	t.Run("logout ends the session; other sessions are unaffected", func(t *testing.T) {
		a := e.client("198.51.100.9")
		b := e.signupAndVerify("arjun@example.in", "198.51.100.10")
		a.post("/v1/auth/login", map[string]string{"email": "priya@example.in", "password": "correct horse battery staple"})
		if r := a.post("/v1/auth/logout", nil); r.Status != http.StatusNoContent {
			t.Fatalf("logout = %d %s", r.Status, r.Body)
		}
		if r := a.get("/v1/me"); r.Status != http.StatusUnauthorized || r.errorCode(t) != "unauthenticated" {
			t.Errorf("after logout /v1/me = %d %s", r.Status, r.Body)
		}
		var me struct{ Email string }
		r := b.get("/v1/me")
		r.json(t, &me)
		if r.Status != http.StatusOK || me.Email != "arjun@example.in" {
			t.Errorf("other user's session affected: %d %s", r.Status, r.Body)
		}
	})

	t.Run("unauthenticated and forged sessions are refused", func(t *testing.T) {
		c := e.client("198.51.100.11")
		if r := c.get("/v1/me"); r.Status != http.StatusUnauthorized {
			t.Errorf("no cookie: %d", r.Status)
		}
		c.http.Jar.SetCookies(urlOf(t, e), []*http.Cookie{{Name: "__Host-tuorps_session", Value: "forged", Secure: true, Path: "/"}})
		if r := c.get("/v1/me"); r.Status != http.StatusUnauthorized {
			t.Errorf("forged cookie: %d", r.Status)
		}
		if r := c.post("/v1/auth/logout", nil); r.Status != http.StatusUnauthorized {
			t.Errorf("logout without a session: %d", r.Status)
		}
	})

	t.Run("resend verification only for unverified accounts, always 202", func(t *testing.T) {
		c := e.client("198.51.100.12")
		c.post("/v1/auth/signup", map[string]string{"email": "late@example.in", "password": "correct horse battery staple", "name": "Late", "workspace_name": "Late Co"})
		before := e.countEmails("late@example.in", emailcore.KindVerifyEmail)
		for _, addr := range []string{"late@example.in", "priya@example.in", "nobody@example.in"} {
			if r := c.post("/v1/auth/verify-email/resend", map[string]string{"email": addr}); r.Status != http.StatusAccepted {
				t.Errorf("resend %s = %d", addr, r.Status)
			}
		}
		if after := e.countEmails("late@example.in", emailcore.KindVerifyEmail); after != before+1 {
			t.Errorf("unverified account got %d new emails", after-before)
		}
		if e.countEmails("Priya@Example.in", emailcore.KindVerifyEmail) != 1 {
			t.Error("verified account was sent another verification email")
		}
	})

	t.Run("rate limits", func(t *testing.T) {
		// Per address, independent of IP: 10 attempts a minute, from anywhere.
		var last response
		for i := range 11 {
			last = e.client(fmt.Sprintf("192.0.2.%d", 100+i)).post("/v1/auth/login", map[string]string{"email": "target@example.in", "password": "not the password"})
		}
		if last.Status != http.StatusTooManyRequests || last.Header.Get("Retry-After") == "" {
			t.Errorf("11th login attempt for one address: %d", last.Status)
		}
		s := e.client("198.51.100.14")
		for i := range 6 {
			last = s.post("/v1/auth/signup", map[string]string{"email": "flood" + string(rune('a'+i)) + "@example.in", "password": "correct horse battery staple", "name": "F", "workspace_name": "F"})
		}
		if last.Status != http.StatusTooManyRequests {
			t.Errorf("6th signup from one IP: %d", last.Status)
		}
	})
}

func TestSecureCookieAttributes(t *testing.T) {
	cookie := authhttp.SessionCookie("tok", 3600)
	s := cookie.String()
	for _, attr := range []string{"__Host-tuorps_session=tok", "Secure", "HttpOnly", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(s, attr) {
			t.Errorf("secure cookie %q missing %s", s, attr)
		}
	}
	if strings.Contains(s, "Domain=") {
		t.Error("__Host- cookies must not set Domain")
	}
}

func urlOf(t *testing.T, e *env) *url.URL {
	t.Helper()
	u, err := url.Parse(e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
