package authhttp

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/auth"
	"github.com/yahya31415/tuorps/internal/auth/core"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (l loginRequest) Validate() error {
	if l.Email == "" || l.Password == "" || len(l.Email) > 254 || len(l.Password) > maxPasswordBody {
		return errors.New("email and password are required")
	}
	return nil
}

type userResponse struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	Locale        string `json:"locale"`
	EmailVerified bool   `json:"email_verified"`
	MFAEnabled    bool   `json:"mfa_enabled"`
}

func invalidCredentials(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
}

// login checks a password. Unknown addresses, wrong passwords and disabled
// accounts get the same response after the same amount of work.
func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, h.rules.loginIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[loginRequest](w, r, maxBody)
	if !ok {
		return
	}
	_, normalized, err := core.NormalizeEmail(req.Email)
	if err != nil {
		h.Hasher.DummyVerify(r.Context(), req.Password)
		invalidCredentials(w, r)
		return
	}
	if !h.allow(w, r, h.rules.loginEmail, normalized) {
		return
	}
	rec, found, err := h.lookup(r.Context(), normalized)
	if err != nil {
		h.internalError(w, r, "lookup", err)
		return
	}
	passwordOK, rehash := false, false
	if found {
		if passwordOK, rehash, err = h.Hasher.Verify(r.Context(), req.Password, rec.PasswordHash); err != nil {
			h.Log.WarnContext(r.Context(), "unusable password hash", "user_id", rec.ID.String())
			passwordOK = false
		}
	} else {
		h.Hasher.DummyVerify(r.Context(), req.Password)
	}

	switch outcome := core.DecideLogin(rec.Account, passwordOK); outcome {
	case core.LoginInvalid, core.LoginDisabled:
		h.Log.InfoContext(r.Context(), "login refused", "outcome", outcome.String(), "request_id", httpx.RequestIDFrom(r.Context()))
		invalidCredentials(w, r)
	case core.LoginEmailNotVerified:
		httpx.WriteError(w, r, http.StatusForbidden, "email_not_verified", "verify your email address first")
	case core.LoginMFARequired:
		challenge, err := h.Challenges.Create(r.Context(), rec.ID)
		if err != nil {
			h.internalError(w, r, "challenge", err)
			return
		}
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]any{"mfa_required": true, "challenge": challenge})
	case core.LoginSuccess:
		newHash := ""
		if rehash {
			if newHash, err = h.Hasher.Hash(r.Context(), req.Password); err != nil {
				h.internalError(w, r, "rehash", err)
				return
			}
		}
		_ = h.completeLogin(w, r, rec, false, func(tx audit.Tx) error {
			if newHash == "" {
				return nil
			}
			return auth.SetPasswordHash(r.Context(), tx, rec.ID, newHash, h.Now())
		})
	}
}

// completeLogin audits the login, runs extra (a second factor or a
// rehash), starts a fresh session and returns the user. It reports success.
func (h *handlers) completeLogin(w http.ResponseWriter, r *http.Request, rec auth.LoginRecord, mfa bool, extra func(audit.Tx) error) bool {
	action := "auth.login"
	if mfa {
		action = "auth.login_mfa"
	}
	var profile userResponse
	err := h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: rec.ID}, event(action, rec.ID), h.meta(r), func(tx audit.Tx) error {
		if err := extra(tx); err != nil {
			return err
		}
		var err error
		profile, err = loadUser(r, tx.Pgx(), rec)
		return err
	})
	switch {
	case errors.Is(err, errInvalidCode):
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_code", errInvalidCode.Error())
		return false
	case err != nil:
		h.internalError(w, r, "login", err)
		return false
	}
	if err := h.startSession(w, r, rec.ID, mfa); err != nil {
		h.internalError(w, r, "session", err)
		return false
	}
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": profile})
	return true
}

func loadUser(r *http.Request, tx pgx.Tx, rec auth.LoginRecord) (userResponse, error) {
	p, err := auth.GetProfile(r.Context(), tx, rec.ID)
	if err != nil {
		return userResponse{}, err
	}
	c, err := auth.GetCredentials(r.Context(), tx, rec.ID)
	if err != nil {
		return userResponse{}, err
	}
	return userResponse{ID: p.ID.String(), Email: p.Email, Name: p.DisplayName, Locale: p.Locale, EmailVerified: p.EmailVerified, MFAEnabled: c.TOTPEnabled}, nil
}

// logout ends the current session.
func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	id, _ := IdentityFrom(r.Context())
	if err := h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: id.UserID}, event("auth.logout", id.UserID), h.meta(r), func(audit.Tx) error {
		return h.Sessions.Delete(r.Context(), id.token)
	}); err != nil {
		h.internalError(w, r, "logout", err)
		return
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// me returns the caller's profile.
func (h *handlers) me(w http.ResponseWriter, r *http.Request) {
	id, _ := IdentityFrom(r.Context())
	var u userResponse
	if err := postgres.InReadTx(r.Context(), h.Pool, postgres.Scope{UserID: id.UserID}, func(tx pgx.Tx) error {
		var err error
		u, err = loadUser(r, tx, auth.LoginRecord{ID: id.UserID})
		return err
	}); err != nil {
		h.internalError(w, r, "me", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, u)
}
