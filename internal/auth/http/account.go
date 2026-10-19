package authhttp

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/auth"
	"github.com/yahya31415/tuorps/internal/auth/core"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/workspace"
	wscore "github.com/yahya31415/tuorps/internal/workspace/core"
)

var errInvalidToken = errors.New("invalid or expired token")

type signupRequest struct {
	Email         string `json:"email"`
	Password      string `json:"password"`
	Name          string `json:"name"`
	WorkspaceName string `json:"workspace_name"`
}

func (s signupRequest) Validate() error {
	if _, _, err := core.NormalizeEmail(s.Email); err != nil {
		return errors.New("email must be a single valid address")
	}
	if _, err := wscore.ValidateName(s.Name); err != nil {
		return errors.New("name must be 1-100 characters")
	}
	if _, err := wscore.ValidateName(s.WorkspaceName); err != nil {
		return errors.New("workspace_name must be 1-100 characters")
	}
	return passwordProblem(s.Password, s.Email)
}

// passwordProblem renders the password policy result as a user-facing error.
func passwordProblem(pw, email string) error {
	if err := core.ValidatePassword(pw, email); err != nil {
		return errors.New(strings.TrimPrefix(err.Error(), "auth: "))
	}
	return nil
}

type tokenRequest struct {
	Token string `json:"token"`
}

func (t tokenRequest) Validate() error {
	if t.Token == "" || len(t.Token) > 128 {
		return errors.New("token is required")
	}
	return nil
}

type emailRequest struct {
	Email string `json:"email"`
}

func (e emailRequest) Validate() error {
	if e.Email == "" || len(e.Email) > 254 {
		return errors.New("email is required")
	}
	return nil
}

// signup creates an unverified account with its own workspace and first
// brand, and emails a verification link. The response never reveals
// whether the address was already registered.
func (h *handlers) signup(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, h.rules.signupIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[signupRequest](w, r, maxBody)
	if !ok {
		return
	}
	display, normalized, _ := core.NormalizeEmail(req.Email)
	name, _ := wscore.ValidateName(req.Name)
	wsName, _ := wscore.ValidateName(req.WorkspaceName)
	hash, err := h.Hasher.Hash(r.Context(), req.Password) // also for duplicates: equal timing
	if err != nil {
		h.internalError(w, r, "hash", err)
		return
	}
	user, ws, brand, now := h.NewID(), h.NewID(), h.NewID(), h.Now()
	err = h.Writer.InWriteTx(r.Context(), postgres.Scope{WorkspaceID: ws, UserID: user}, event("auth.signup", user), h.meta(r), func(tx audit.Tx) error {
		if err := auth.CreateAccount(r.Context(), tx, auth.NewAccount{ID: user, Email: display, EmailNormalized: normalized, DisplayName: name, Locale: "en", PasswordHash: hash, Now: now}); err != nil {
			return err
		}
		if err := h.Bootstrap(r.Context(), tx, workspace.NewWorkspace{ID: ws, Name: wsName, Owner: user, BrandID: brand, BrandName: wsName, Now: now}); err != nil {
			return err
		}
		return h.sendVerification(r.Context(), tx, auth.LoginRecord{ID: user, Email: display, DisplayName: name, Locale: "en"})
	})
	switch {
	case errors.Is(err, auth.ErrEmailTaken):
		if err := h.notifyExisting(r, normalized); err != nil {
			h.Log.ErrorContext(r.Context(), "notify existing account failed", "err", err.Error())
		}
	case err != nil:
		h.internalError(w, r, "signup", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "check_email"})
}

// sendVerification issues a verification token and queues the email.
func (h *handlers) sendVerification(ctx context.Context, tx audit.Tx, u auth.LoginRecord) error {
	tok, hash, err := h.newToken()
	if err != nil {
		return err
	}
	now := h.Now()
	if err := auth.IssueEmailToken(ctx, tx, h.NewID(), u.ID, auth.PurposeVerifyEmail, hash, now.Add(verifyTokenTTL), now); err != nil {
		return err
	}
	m, err := emailcore.NewMessage(mail.Address{Name: u.DisplayName, Address: u.Email}, emailcore.KindVerifyEmail, u.Locale,
		map[string]string{"name": u.DisplayName, "link": emailcore.Link(h.Origin, "/verify-email", map[string]string{"token": tok})})
	if err != nil {
		return err
	}
	return h.Mailer.Enqueue(ctx, tx, m)
}

// notifyExisting tells an account owner that someone tried to sign up with
// their address, pointing them to password reset.
func (h *handlers) notifyExisting(r *http.Request, normalized string) error {
	rec, found, err := h.lookup(r.Context(), normalized)
	if err != nil || !found {
		return err
	}
	return h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: rec.ID}, event("auth.signup_existing", rec.ID), h.meta(r), func(tx audit.Tx) error {
		m, err := emailcore.NewMessage(mail.Address{Name: rec.DisplayName, Address: rec.Email}, emailcore.KindAccountExists, rec.Locale,
			map[string]string{"name": rec.DisplayName, "link": emailcore.Link(h.Origin, "/forgot-password", map[string]string{"email": rec.Email})})
		if err != nil {
			return err
		}
		return h.Mailer.Enqueue(r.Context(), tx, m)
	})
}

func (h *handlers) lookup(ctx context.Context, normalized string) (auth.LoginRecord, bool, error) {
	var rec auth.LoginRecord
	var found bool
	err := postgres.InReadTx(ctx, h.Pool, postgres.Scope{}, func(tx pgx.Tx) error {
		var err error
		rec, found, err = auth.LookupLogin(ctx, tx, normalized)
		return err
	})
	return rec, found, err
}

// verifyEmail redeems a verification token.
func (h *handlers) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, h.rules.tokenIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[tokenRequest](w, r, maxBody)
	if !ok {
		return
	}
	hash, valid := core.HashToken(req.Token)
	if !valid {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_token", errInvalidToken.Error())
		return
	}
	now := h.Now()
	err := h.Writer.InWriteTxResolving(r.Context(),
		func(tx pgx.Tx) (postgres.Scope, error) {
			user, ok, err := auth.RedeemEmailToken(r.Context(), tx, hash, auth.PurposeVerifyEmail, now)
			if err != nil || !ok {
				return postgres.Scope{}, errors.Join(err, errInvalidToken)
			}
			return postgres.Scope{UserID: user}, nil
		},
		func(s postgres.Scope) (auditEvent, error) { return event("auth.email_verified", s.UserID), nil },
		h.meta(r),
		func(tx audit.Tx) error { return auth.MarkEmailVerified(r.Context(), tx, tx.Scope().UserID, now) })
	switch {
	case errors.Is(err, errInvalidToken):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_token", errInvalidToken.Error())
	case err != nil:
		h.internalError(w, r, "verify_email", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// resendVerification re-sends the link to an unverified account; the
// response is the same whatever the address.
func (h *handlers) resendVerification(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, h.rules.emailIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[emailRequest](w, r, maxBody)
	if !ok {
		return
	}
	defer func() { _ = httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "check_email"}) }()
	_, normalized, err := core.NormalizeEmail(req.Email)
	if err != nil {
		return
	}
	if d, err := h.Limiter.Allow(r.Context(), h.rules.emailAddr, normalized); err != nil || !d.Allowed {
		return // silently: a 429 here would reveal nothing useful but wastes the user's attempts
	}
	rec, found, err := h.lookup(r.Context(), normalized)
	if err != nil || !found || rec.Account.EmailVerified || rec.Account.Disabled {
		return
	}
	if err := h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: rec.ID}, event("auth.verification_resent", rec.ID), h.meta(r), func(tx audit.Tx) error {
		return h.sendVerification(r.Context(), tx, rec)
	}); err != nil {
		h.Log.ErrorContext(r.Context(), "resend verification failed", "err", err.Error())
	}
}
