package authhttp_test

import (
	"net/http"
	"testing"

	emailcore "github.com/yahya31415/tuorps/internal/email/core"
)

func TestPasswordsProfileAndSessions(t *testing.T) {
	e := newEnv(t)
	a := e.signupAndVerify("asha@example.in", "203.0.113.20")
	b := e.signupAndVerify("bina@example.in", "203.0.113.21")

	t.Run("change password: checks, revokes other sessions, keeps this one", func(t *testing.T) {
		second := e.client("203.0.113.22")
		login(second, "asha@example.in")
		if r := a.post("/v1/me/password", map[string]string{"current_password": "wrong password!!", "new_password": "a brand new passphrase"}); r.Status != http.StatusForbidden || r.errorCode(t) != "invalid_password" {
			t.Errorf("wrong current: %d %s", r.Status, r.Body)
		}
		if r := a.post("/v1/me/password", map[string]string{"current_password": "correct horse battery staple", "new_password": "password1234"}); r.Status != http.StatusUnprocessableEntity {
			t.Errorf("weak new password: %d", r.Status)
		}
		if r := a.post("/v1/me/password", map[string]string{"current_password": "correct horse battery staple", "new_password": "a brand new passphrase"}); r.Status != http.StatusNoContent {
			t.Fatalf("change = %d %s", r.Status, r.Body)
		}
		if r := a.get("/v1/me"); r.Status != http.StatusOK {
			t.Error("current session was revoked")
		}
		if r := second.get("/v1/me"); r.Status != http.StatusUnauthorized {
			t.Error("other session survived the password change")
		}
		if r := login(e.client("203.0.113.23"), "asha@example.in"); r.Status != http.StatusUnauthorized {
			t.Error("old password still works")
		}
		if r := e.client("203.0.113.24").post("/v1/auth/login", map[string]string{"email": "asha@example.in", "password": "a brand new passphrase"}); r.Status != http.StatusOK {
			t.Errorf("new password: %d", r.Status)
		}
		if e.countEmails("asha@example.in", emailcore.KindPasswordChanged) != 1 {
			t.Error("no notice email")
		}
		if r := b.get("/v1/me"); r.Status != http.StatusOK {
			t.Error("another user's session was revoked")
		}
	})

	t.Run("forgot password reveals nothing and emails existing accounts", func(t *testing.T) {
		c := e.client("203.0.113.25")
		for _, addr := range []string{"bina@example.in", "ghost@example.in"} {
			if r := c.post("/v1/auth/password/forgot", map[string]string{"email": addr}); r.Status != http.StatusAccepted {
				t.Errorf("forgot %s = %d", addr, r.Status)
			}
		}
		if e.countEmails("bina@example.in", emailcore.KindPasswordReset) != 1 || e.countEmails("ghost@example.in", emailcore.KindPasswordReset) != 0 {
			t.Error("reset emails wrong")
		}
	})

	t.Run("reset: policy failure keeps the token; success revokes all sessions; token single-use", func(t *testing.T) {
		c := e.client("203.0.113.26")
		tok := tokenFrom(t, e.lastEmailTo("bina@example.in", emailcore.KindPasswordReset))
		if r := c.post("/v1/auth/password/reset", map[string]string{"token": tok, "new_password": "bina1234567890"}); r.Status != http.StatusUnprocessableEntity {
			t.Fatalf("personal password: %d %s", r.Status, r.Body)
		}
		if r := c.post("/v1/auth/password/reset", map[string]string{"token": tok, "new_password": "fresh start passphrase"}); r.Status != http.StatusNoContent {
			t.Fatalf("reset = %d %s", r.Status, r.Body)
		}
		if r := c.post("/v1/auth/password/reset", map[string]string{"token": tok, "new_password": "another passphrase here"}); r.Status != http.StatusBadRequest || r.errorCode(t) != "invalid_token" {
			t.Errorf("reused token: %d %s", r.Status, r.Body)
		}
		if r := b.get("/v1/me"); r.Status != http.StatusUnauthorized {
			t.Error("sessions survived the reset")
		}
		if r := e.client("203.0.113.27").post("/v1/auth/login", map[string]string{"email": "bina@example.in", "password": "fresh start passphrase"}); r.Status != http.StatusOK {
			t.Errorf("login with the new password: %d", r.Status)
		}
		if r := c.post("/v1/auth/password/reset", map[string]string{"token": "not-a-token", "new_password": "fresh start passphrase"}); r.Status != http.StatusBadRequest {
			t.Errorf("garbage token: %d", r.Status)
		}
	})

	t.Run("reset also verifies an unverified address", func(t *testing.T) {
		c := e.client("203.0.113.28")
		c.post("/v1/auth/signup", map[string]string{"email": "unv@example.in", "password": "correct horse battery staple", "name": "U", "workspace_name": "U"})
		c.post("/v1/auth/password/forgot", map[string]string{"email": "unv@example.in"})
		tok := tokenFrom(t, e.lastEmailTo("unv@example.in", emailcore.KindPasswordReset))
		if r := c.post("/v1/auth/password/reset", map[string]string{"token": tok, "new_password": "fresh start passphrase"}); r.Status != http.StatusNoContent {
			t.Fatalf("reset = %d", r.Status)
		}
		if r := c.post("/v1/auth/login", map[string]string{"email": "unv@example.in", "password": "fresh start passphrase"}); r.Status != http.StatusOK {
			t.Errorf("login after reset: %d %s", r.Status, r.Body)
		}
	})

	t.Run("update profile", func(t *testing.T) {
		r := a.do(http.MethodPatch, "/v1/me", map[string]string{"name": "Asha K", "locale": "ml"})
		var u struct{ Name, Locale string }
		r.json(t, &u)
		if r.Status != http.StatusOK || u.Name != "Asha K" || u.Locale != "ml" {
			t.Fatalf("patch = %d %s", r.Status, r.Body)
		}
		for name, body := range map[string]any{"empty": map[string]string{}, "bad locale": map[string]string{"locale": "xx"}, "bad name": map[string]string{"name": ""}} {
			if r := a.do(http.MethodPatch, "/v1/me", body); r.Status != http.StatusUnprocessableEntity {
				t.Errorf("%s: %d", name, r.Status)
			}
		}
		fresh := e.client("203.0.113.29")
		fresh.post("/v1/auth/login", map[string]string{"email": "bina@example.in", "password": "fresh start passphrase"})
		var other struct{ Name string }
		fresh.get("/v1/me").json(t, &other)
		if other.Name == "Asha K" {
			t.Error("profile change leaked to another user")
		}
	})

	t.Run("logout everywhere", func(t *testing.T) {
		x, y := e.client("203.0.113.30"), e.client("203.0.113.31")
		x.post("/v1/auth/login", map[string]string{"email": "bina@example.in", "password": "fresh start passphrase"})
		y.post("/v1/auth/login", map[string]string{"email": "bina@example.in", "password": "fresh start passphrase"})
		if r := x.post("/v1/auth/logout-all", nil); r.Status != http.StatusNoContent {
			t.Fatalf("logout-all = %d", r.Status)
		}
		if x.get("/v1/me").Status != http.StatusUnauthorized || y.get("/v1/me").Status != http.StatusUnauthorized {
			t.Error("a session survived logout-all")
		}
		if a.get("/v1/me").Status != http.StatusOK {
			t.Error("another user's session was ended")
		}
	})
}
