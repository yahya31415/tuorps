package authhttp

import (
	"errors"
	"net/http"
	"net/mail"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/auth"
	"github.com/yahya31415/tuorps/internal/auth/core"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	wscore "github.com/yahya31415/tuorps/internal/workspace/core"
)

// supportedLocales are the interface languages planned (features.md).
func supportedLocales() []string { return []string{"en", "hi", "ml", "ta", "kn", "te", "mr", "bn"} }

// errPolicy wraps a password-policy failure detected inside a transaction.
type errPolicy struct{ err error }

func (e errPolicy) Error() string { return e.err.Error() }

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (c changePasswordRequest) Validate() error {
	if c.CurrentPassword == "" || len(c.CurrentPassword) > maxPasswordBody || len(c.NewPassword) > maxPasswordBody {
		return errors.New("current_password and new_password are required")
	}
	return passwordProblem(c.NewPassword, "")
}

type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (p resetPasswordRequest) Validate() error {
	if p.Token == "" || len(p.Token) > 128 || len(p.NewPassword) > maxPasswordBody {
		return errors.New("token and new_password are required")
	}
	return passwordProblem(p.NewPassword, "")
}

type updateProfileRequest struct {
	Name   *string `json:"name,omitempty"`
	Locale *string `json:"locale,omitempty"`
}

func (u updateProfileRequest) Validate() error {
	if u.Name == nil && u.Locale == nil {
		return errors.New("provide name or locale")
	}
	if u.Name != nil {
		if _, err := wscore.ValidateName(*u.Name); err != nil {
			return errors.New("name must be 1-100 characters")
		}
	}
	if u.Locale != nil && !slices.Contains(supportedLocales(), *u.Locale) {
		return errors.New("locale is not supported")
	}
	return nil
}

// changePassword replaces the password and ends the user's other sessions.
func (h *handlers) changePassword(w http.ResponseWriter, r *http.Request) {
	id, _ := IdentityFrom(r.Context())
	if !h.allow(w, r, h.rules.mfaIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[changePasswordRequest](w, r, maxBody)
	if !ok {
		return
	}
	if _, err := h.checkPassword(r.Context(), id.UserID, req.CurrentPassword); err != nil {
		if !mfaError(w, r, err) {
			h.internalError(w, r, "password_change", err)
		}
		return
	}
	hash, err := h.Hasher.Hash(r.Context(), req.NewPassword)
	if err != nil {
		h.internalError(w, r, "hash", err)
		return
	}
	err = h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: id.UserID}, event("auth.password_change", id.UserID), h.meta(r), func(tx audit.Tx) error {
		return h.setPassword(r, tx, req.NewPassword, hash)
	})
	if !h.passwordWriteResult(w, r, err) {
		return
	}
	if err := h.Sessions.DeleteAllForUser(r.Context(), id.UserID, id.token); err != nil {
		h.internalError(w, r, "revoke_sessions", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setPassword applies the personal part of the policy (it needs the email),
// stores the hash and sends the notice.
func (h *handlers) setPassword(r *http.Request, tx audit.Tx, pw, hash string) error {
	user := tx.Scope().UserID
	p, err := auth.GetProfile(r.Context(), tx.Pgx(), user)
	if err != nil {
		return err
	}
	if err := passwordProblem(pw, p.Email); err != nil {
		return errPolicy{err}
	}
	if err := auth.SetPasswordHash(r.Context(), tx, user, hash, h.Now()); err != nil {
		return err
	}
	return h.notify(r.Context(), tx, user, emailcore.KindPasswordChanged)
}

func (h *handlers) passwordWriteResult(w http.ResponseWriter, r *http.Request, err error) bool {
	var policy errPolicy
	switch {
	case err == nil:
		return true
	case errors.As(err, &policy):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, "validation_failed", policy.Error())
	case errors.Is(err, errInvalidToken):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_token", errInvalidToken.Error())
	default:
		h.internalError(w, r, "password", err)
	}
	return false
}

// forgotPassword emails a reset link to an existing account; the response
// is the same whatever the address.
func (h *handlers) forgotPassword(w http.ResponseWriter, r *http.Request) {
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
		return
	}
	rec, found, err := h.lookup(r.Context(), normalized)
	if err != nil || !found || rec.Account.Disabled {
		return
	}
	if err := h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: rec.ID}, event("auth.password_reset_request", rec.ID), h.meta(r), func(tx audit.Tx) error {
		tok, hash, err := h.newToken()
		if err != nil {
			return err
		}
		now := h.Now()
		if err := auth.IssueEmailToken(r.Context(), tx, h.NewID(), rec.ID, auth.PurposeResetPassword, hash, now.Add(resetTokenTTL), now); err != nil {
			return err
		}
		m, err := emailcore.NewMessage(mail.Address{Name: rec.DisplayName, Address: rec.Email}, emailcore.KindPasswordReset, rec.Locale,
			map[string]string{"name": rec.DisplayName, "link": emailcore.Link(h.Origin, "/reset-password", map[string]string{"token": tok})})
		if err != nil {
			return err
		}
		return h.Mailer.Enqueue(r.Context(), tx, m)
	}); err != nil {
		h.Log.ErrorContext(r.Context(), "password reset request failed", "err", err.Error())
	}
}

// resetPassword redeems a reset token and sets a new password in one
// transaction (a policy failure leaves the token unused), then ends every
// session of the account.
func (h *handlers) resetPassword(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, h.rules.tokenIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[resetPasswordRequest](w, r, maxBody)
	if !ok {
		return
	}
	tokenHash, valid := core.HashToken(req.Token)
	if !valid {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_token", errInvalidToken.Error())
		return
	}
	hash, err := h.Hasher.Hash(r.Context(), req.NewPassword)
	if err != nil {
		h.internalError(w, r, "hash", err)
		return
	}
	now := h.Now()
	var user postgres.Scope
	err = h.Writer.InWriteTxResolving(r.Context(),
		func(tx pgx.Tx) (postgres.Scope, error) {
			u, ok, err := auth.RedeemEmailToken(r.Context(), tx, tokenHash, auth.PurposeResetPassword, now)
			if err != nil || !ok {
				return postgres.Scope{}, errors.Join(err, errInvalidToken)
			}
			user = postgres.Scope{UserID: u}
			return user, nil
		},
		func(s postgres.Scope) (auditEvent, error) { return event("auth.password_reset", s.UserID), nil },
		h.meta(r),
		func(tx audit.Tx) error {
			if err := h.setPassword(r, tx, req.NewPassword, hash); err != nil {
				return err
			}
			return auth.MarkEmailVerified(r.Context(), tx, tx.Scope().UserID, now) // the reset proved control of the address
		})
	if !h.passwordWriteResult(w, r, err) {
		return
	}
	if err := h.Sessions.DeleteAllForUser(r.Context(), user.UserID, ""); err != nil {
		h.internalError(w, r, "revoke_sessions", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateProfile changes the caller's display name and/or locale.
func (h *handlers) updateProfile(w http.ResponseWriter, r *http.Request) {
	id, _ := IdentityFrom(r.Context())
	req, ok := httpx.Decode[updateProfileRequest](w, r, maxBody)
	if !ok {
		return
	}
	var u userResponse
	err := h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: id.UserID}, event("auth.profile_update", id.UserID), h.meta(r), func(tx audit.Tx) error {
		p, err := auth.GetProfile(r.Context(), tx.Pgx(), id.UserID)
		if err != nil {
			return err
		}
		name, locale := p.DisplayName, p.Locale
		if req.Name != nil {
			name, _ = wscore.ValidateName(*req.Name)
		}
		if req.Locale != nil {
			locale = *req.Locale
		}
		if err := auth.UpdateProfile(r.Context(), tx, id.UserID, name, locale, h.Now()); err != nil {
			return err
		}
		u, err = loadUser(r, tx.Pgx(), auth.LoginRecord{ID: id.UserID})
		return err
	})
	if err != nil {
		h.internalError(w, r, "profile_update", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, u)
}

// logoutAll ends every session of the caller, including this one.
func (h *handlers) logoutAll(w http.ResponseWriter, r *http.Request) {
	id, _ := IdentityFrom(r.Context())
	if err := h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: id.UserID}, event("auth.logout_all", id.UserID), h.meta(r), func(audit.Tx) error {
		return h.Sessions.DeleteAllForUser(r.Context(), id.UserID, "")
	}); err != nil {
		h.internalError(w, r, "logout_all", err)
		return
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
