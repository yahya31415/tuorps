package authhttp_test

import (
	"encoding/base32"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/auth/core"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
)

// codeAt is the TOTP code offset steps from now.
func codeAt(t *testing.T, secret string, offset int64) string {
	t.Helper()
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return core.TOTPCode(raw, uint64(core.TOTPStep(time.Now())+offset))
}

func login(c *client, email string) response {
	return c.post("/v1/auth/login", map[string]string{"email": email, "password": "correct horse battery staple"})
}

func TestTwoFactor(t *testing.T) {
	e := newEnv(t)
	c := e.signupAndVerify("mfa@example.in", "203.0.113.1")
	other := e.signupAndVerify("plain@example.in", "203.0.113.2")

	var setup struct {
		Secret     string `json:"secret"`
		OTPAuthURI string `json:"otpauth_uri"`
	}
	var recovery []string

	t.Run("setup requires the password", func(t *testing.T) {
		if r := c.post("/v1/me/mfa/totp", map[string]string{"password": "wrong password!!"}); r.Status != http.StatusForbidden || r.errorCode(t) != "invalid_password" {
			t.Fatalf("wrong password: %d %s", r.Status, r.Body)
		}
		r := c.post("/v1/me/mfa/totp", map[string]string{"password": "correct horse battery staple"})
		r.json(t, &setup)
		if r.Status != http.StatusOK || len(setup.Secret) < 32 || !strings.HasPrefix(setup.OTPAuthURI, "otpauth://totp/Tuorps:mfa@example.in?") {
			t.Fatalf("setup = %d %s", r.Status, r.Body)
		}
	})

	t.Run("the secret is stored encrypted", func(t *testing.T) {
		var enc string
		if err := e.admin.QueryRow(t.Context(), `SELECT c.totp_secret_enc FROM tuorps.user_credentials c JOIN tuorps.users u ON u.id = c.user_id WHERE u.email_normalized = 'mfa@example.in'`).Scan(&enc); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(enc, "v1.k1.") || strings.Contains(enc, setup.Secret) {
			t.Errorf("stored secret %q is not a keyring encoding", enc)
		}
	})

	t.Run("confirm with the right code enables 2FA and returns recovery codes", func(t *testing.T) {
		if r := c.post("/v1/me/mfa/totp/confirm", map[string]string{"code": "000000"}); r.Status != http.StatusBadRequest || r.errorCode(t) != "invalid_code" {
			t.Fatalf("wrong code: %d %s", r.Status, r.Body)
		}
		r := c.post("/v1/me/mfa/totp/confirm", map[string]string{"code": codeAt(t, setup.Secret, -1)})
		var body struct {
			RecoveryCodes []string `json:"recovery_codes"`
		}
		r.json(t, &body)
		if r.Status != http.StatusOK || len(body.RecoveryCodes) != 10 {
			t.Fatalf("confirm = %d %s", r.Status, r.Body)
		}
		recovery = body.RecoveryCodes
		var me struct {
			MFAEnabled bool `json:"mfa_enabled"`
		}
		c.get("/v1/me").json(t, &me)
		if !me.MFAEnabled {
			t.Error("/v1/me does not show 2FA enabled")
		}
		if e.countEmails("mfa@example.in", emailcore.KindMFAEnabled) != 1 {
			t.Error("no notice email")
		}
		if r := c.post("/v1/me/mfa/totp", map[string]string{"password": "correct horse battery staple"}); r.Status != http.StatusConflict {
			t.Errorf("setup while enabled: %d", r.Status)
		}
	})

	var challenge string
	t.Run("login now needs a second factor", func(t *testing.T) {
		fresh := e.client("203.0.113.3")
		r := login(fresh, "mfa@example.in")
		var body struct {
			MFARequired bool   `json:"mfa_required"`
			Challenge   string `json:"challenge"`
		}
		r.json(t, &body)
		if r.Status != http.StatusOK || !body.MFARequired || body.Challenge == "" || r.Header.Get("Set-Cookie") != "" {
			t.Fatalf("login = %d %s (cookie %q)", r.Status, r.Body, r.Header.Get("Set-Cookie"))
		}
		if me := fresh.get("/v1/me"); me.Status != http.StatusUnauthorized {
			t.Error("a session exists before the second factor")
		}
		challenge = body.Challenge
		if r := fresh.post("/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": "000000"}); r.Status != http.StatusUnauthorized || r.errorCode(t) != "invalid_code" {
			t.Errorf("wrong code: %d %s", r.Status, r.Body)
		}
		code := codeAt(t, setup.Secret, 0)
		if r := fresh.post("/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": code}); r.Status != http.StatusOK {
			t.Fatalf("right code: %d %s", r.Status, r.Body)
		}
		if me := fresh.get("/v1/me"); me.Status != http.StatusOK {
			t.Error("no session after the second factor")
		}

		replay := e.client("203.0.113.4")
		var again struct{ Challenge string }
		login(replay, "mfa@example.in").json(t, &again)
		if r := replay.post("/v1/auth/login/mfa", map[string]string{"challenge": again.Challenge, "code": code}); r.Status != http.StatusUnauthorized {
			t.Errorf("replayed code accepted: %d", r.Status)
		}
	})

	t.Run("a consumed challenge cannot be reused", func(t *testing.T) {
		x := e.client("203.0.113.5")
		if r := x.post("/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": codeAt(t, setup.Secret, 1)}); r.Status != http.StatusUnauthorized || r.errorCode(t) != "challenge_invalid" {
			t.Errorf("reused challenge: %d %s", r.Status, r.Body)
		}
	})

	t.Run("recovery codes work once", func(t *testing.T) {
		x := e.client("203.0.113.6")
		var ch struct{ Challenge string }
		login(x, "mfa@example.in").json(t, &ch)
		if r := x.post("/v1/auth/login/mfa", map[string]string{"challenge": ch.Challenge, "recovery_code": strings.ToUpper(recovery[0])}); r.Status != http.StatusOK {
			t.Fatalf("recovery code: %d %s", r.Status, r.Body)
		}
		y := e.client("203.0.113.7")
		login(y, "mfa@example.in").json(t, &ch)
		if r := y.post("/v1/auth/login/mfa", map[string]string{"challenge": ch.Challenge, "recovery_code": recovery[0]}); r.Status != http.StatusUnauthorized {
			t.Errorf("reused recovery code: %d", r.Status)
		}
	})

	t.Run("challenges allow a few attempts only", func(t *testing.T) {
		x := e.client("203.0.113.8")
		var ch struct{ Challenge string }
		login(x, "mfa@example.in").json(t, &ch)
		var last response
		for range 6 {
			last = x.post("/v1/auth/login/mfa", map[string]string{"challenge": ch.Challenge, "code": "000000"})
		}
		if last.Status != http.StatusUnauthorized || last.errorCode(t) != "challenge_invalid" {
			t.Errorf("6th attempt: %d %s", last.Status, last.Body)
		}
	})

	t.Run("regenerate recovery codes needs password and a second factor", func(t *testing.T) {
		if r := c.post("/v1/me/mfa/recovery-codes", map[string]string{"password": "correct horse battery staple"}); r.Status != http.StatusUnprocessableEntity {
			t.Errorf("without a second factor: %d %s", r.Status, r.Body)
		}
		r := c.post("/v1/me/mfa/recovery-codes", map[string]string{"password": "correct horse battery staple", "recovery_code": recovery[1]})
		var body struct {
			RecoveryCodes []string `json:"recovery_codes"`
		}
		r.json(t, &body)
		if r.Status != http.StatusOK || len(body.RecoveryCodes) != 10 {
			t.Fatalf("regenerate = %d %s", r.Status, r.Body)
		}
		x := e.client("203.0.113.9")
		var ch struct{ Challenge string }
		login(x, "mfa@example.in").json(t, &ch)
		if r := x.post("/v1/auth/login/mfa", map[string]string{"challenge": ch.Challenge, "recovery_code": recovery[2]}); r.Status != http.StatusUnauthorized {
			t.Errorf("old recovery code still valid: %d", r.Status)
		}
		recovery = body.RecoveryCodes
	})

	t.Run("the other user is unaffected", func(t *testing.T) {
		var me struct {
			MFAEnabled bool `json:"mfa_enabled"`
		}
		other.get("/v1/me").json(t, &me)
		if me.MFAEnabled {
			t.Error("2FA leaked to another account")
		}
		if r := login(e.client("203.0.113.10"), "plain@example.in"); r.Status != http.StatusOK || strings.Contains(string(r.Body), "mfa_required") {
			t.Errorf("other user's login: %d %s", r.Status, r.Body)
		}
	})

	t.Run("disable needs password and a second factor", func(t *testing.T) {
		if r := c.do(http.MethodDelete, "/v1/me/mfa/totp", map[string]string{"password": "wrong password!!", "recovery_code": recovery[0]}); r.Status != http.StatusForbidden {
			t.Errorf("wrong password: %d", r.Status)
		}
		if r := c.do(http.MethodDelete, "/v1/me/mfa/totp", map[string]string{"password": "correct horse battery staple", "code": "000000"}); r.Status != http.StatusBadRequest || r.errorCode(t) != "invalid_code" {
			t.Errorf("wrong code: %d %s", r.Status, r.Body)
		}
		if r := c.do(http.MethodDelete, "/v1/me/mfa/totp", map[string]string{"password": "correct horse battery staple", "code": codeAt(t, setup.Secret, 1)}); r.Status != http.StatusNoContent {
			t.Fatalf("disable = %d %s", r.Status, r.Body)
		}
		if r := login(e.client("203.0.113.11"), "mfa@example.in"); r.Status != http.StatusOK || strings.Contains(string(r.Body), "mfa_required") {
			t.Errorf("login after disable: %d %s", r.Status, r.Body)
		}
		if e.countEmails("mfa@example.in", emailcore.KindMFADisabled) != 1 {
			t.Error("no notice email")
		}
		if e.count(`SELECT count(*) FROM tuorps.recovery_codes rc JOIN tuorps.users u ON u.id = rc.user_id WHERE u.email_normalized = 'mfa@example.in'`) != 0 {
			t.Error("recovery codes left behind")
		}
	})
}
