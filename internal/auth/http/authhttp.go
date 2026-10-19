// Package authhttp serves the authentication API: signup, email
// verification, login (with TOTP), sessions, password and 2FA management.
// Handlers decode, rate limit, call the auth core and adapters, and encode.
package authhttp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/auth"
	authcore "github.com/yahya31415/tuorps/internal/auth/core"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/clientip"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit"
	ratecore "github.com/yahya31415/tuorps/internal/platform/ratelimit/core"
	"github.com/yahya31415/tuorps/internal/workspace"
)

const (
	maxBody         = 16 << 10
	verifyTokenTTL  = 24 * time.Hour
	resetTokenTTL   = time.Hour
	auditIPPurpose  = "audit-ip"
	maxPasswordBody = 1024 // bytes; bounds argon2 input
)

// Mailer queues an email in an audited write transaction.
type Mailer interface {
	Enqueue(ctx context.Context, tx audit.Tx, m emailcore.Message) error
}

// Deps are the handlers' collaborators.
type Deps struct {
	Pool        *pgxpool.Pool
	Writer      audit.Writer
	Hasher      *auth.Hasher
	Sessions    auth.SessionStore
	Challenges  auth.ChallengeStore
	Mailer      Mailer
	Keys        *keyring.Keyring
	Limiter     ratelimit.Limiter
	ClientIP    clientip.Resolver
	Origin      *url.URL
	SessionLife time.Duration // cookie Max-Age; the server enforces the real limits
	Now         clock.Now
	NewID       ids.NewID
	Rand        io.Reader
	Log         *slog.Logger
	Bootstrap   func(ctx context.Context, tx audit.Tx, nw workspace.NewWorkspace) error
}

// rules are the rate limits on authentication routes. Auth limits fail
// closed: if Redis is unavailable, these routes refuse rather than allow
// unlimited guessing.
type rules struct {
	signupIP, loginIP, loginEmail, mfaIP, emailIP, emailAddr, tokenIP ratelimit.Rule
}

func newRules() (rules, error) {
	mk := func(class string, max int, window time.Duration) (ratelimit.Rule, error) {
		l, err := ratecore.NewLimit(max, window)
		return ratelimit.Rule{Class: class, Limit: l, FailClosed: true}, err
	}
	var r rules
	var errs []error
	add := func(dst *ratelimit.Rule, class string, max int, window time.Duration) {
		rule, err := mk(class, max, window)
		*dst = rule
		errs = append(errs, err)
	}
	add(&r.signupIP, "signup_ip", 5, time.Hour)
	add(&r.loginIP, "login_ip", 10, time.Minute)
	add(&r.loginEmail, "login_email", 5, time.Minute)
	add(&r.mfaIP, "mfa_ip", 20, time.Minute)
	add(&r.emailIP, "email_flow_ip", 5, time.Hour)
	add(&r.emailAddr, "email_flow_addr", 3, time.Hour)
	add(&r.tokenIP, "token_ip", 30, time.Minute)
	return r, errors.Join(errs...)
}

type handlers struct {
	Deps
	rules rules
	ipKey []byte
}

// Register mounts the authentication routes on rt.
func Register(rt *httpx.Router, d Deps) error {
	r, err := newRules()
	if err != nil {
		return err
	}
	h := &handlers{Deps: d, rules: r, ipKey: d.Keys.DeriveKey(auditIPPurpose)}
	authed := RequireSession(d.Sessions)

	rt.HandleFunc("POST /v1/auth/signup", h.signup)
	rt.HandleFunc("POST /v1/auth/verify-email", h.verifyEmail)
	rt.HandleFunc("POST /v1/auth/verify-email/resend", h.resendVerification)
	rt.HandleFunc("POST /v1/auth/login", h.login)
	rt.Handle("POST /v1/auth/logout", authed(http.HandlerFunc(h.logout)))
	rt.Handle("GET /v1/me", authed(http.HandlerFunc(h.me)))
	return nil
}

// allow applies rule to subject, writing the rejection if it fails.
func (h *handlers) allow(w http.ResponseWriter, r *http.Request, rule ratelimit.Rule, subject string) bool {
	d, err := h.Limiter.Allow(r.Context(), rule, subject)
	if err != nil || !d.Allowed {
		ratelimit.Reject(w, r, d, err)
		return false
	}
	return true
}

func (h *handlers) clientIP(r *http.Request) string {
	return h.ClientIP.Resolve(r.RemoteAddr, r.Header.Values("X-Forwarded-For")).String()
}

func (h *handlers) meta(r *http.Request) auditcore.Meta {
	return audit.MetaFromRequest(r, h.ClientIP, h.ipKey)
}

// auditEvent abbreviates the audit event type in handler signatures.
type auditEvent = auditcore.Event

// event builds an account event; its inputs are constants and IDs, so it
// cannot fail validation.
func event(action string, user uuid.UUID) auditcore.Event {
	ev, _ := auditcore.NewEvent(action, "user", user.String(), nil)
	return ev
}

// newToken returns a fresh one-time token and its hash.
func (h *handlers) newToken() (string, [32]byte, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(h.Rand, raw); err != nil {
		return "", [32]byte{}, err
	}
	tok, err := authcore.EncodeToken(raw)
	if err != nil {
		return "", [32]byte{}, err
	}
	hash, _ := authcore.HashToken(tok)
	return tok, hash, nil
}

func (h *handlers) internalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.Log.ErrorContext(r.Context(), "auth handler failed", "op", op, "err", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
	httpx.WriteError(w, r, http.StatusInternalServerError, "internal", "internal error")
}
