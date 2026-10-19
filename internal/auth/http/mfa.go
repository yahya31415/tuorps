package authhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/mail"

	"github.com/jackc/pgx/v5"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/auth"
	"github.com/yahya31415/tuorps/internal/auth/core"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"uuid"
)

const (
	totpSecretBytes = 20 // RFC 4226 recommends 160 bits
	totpIssuer      = "Tuorps"
)

var (
	errInvalidCode     = errors.New("the code is not valid")
	errInvalidPassword = errors.New("the password is not correct")
	errAlreadyEnabled  = errors.New("two-factor authentication is already on")
	errNotEnabled      = errors.New("two-factor authentication is not on")
	errNoPendingSetup  = errors.New("start two-factor setup first")
)

type passwordRequest struct {
	Password string `json:"password"`
}

func (p passwordRequest) Validate() error {
	if p.Password == "" || len(p.Password) > maxPasswordBody {
		return errors.New("password is required")
	}
	return nil
}

type codeRequest struct {
	Code string `json:"code"`
}

func (c codeRequest) Validate() error {
	if c.Code == "" || len(c.Code) > 16 {
		return errors.New("code is required")
	}
	return nil
}

// secondFactor is a TOTP code or a recovery code, exactly one.
type secondFactor struct {
	Code         string `json:"code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

func (s secondFactor) validate() error {
	if (s.Code == "") == (s.RecoveryCode == "") || len(s.Code) > 16 || len(s.RecoveryCode) > 32 {
		return errors.New("provide either code or recovery_code")
	}
	return nil
}

type secondFactorRequest struct {
	Password     string `json:"password"`
	Code         string `json:"code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

func (s secondFactorRequest) Validate() error {
	if err := (passwordRequest{Password: s.Password}).Validate(); err != nil {
		return err
	}
	return secondFactor{Code: s.Code, RecoveryCode: s.RecoveryCode}.validate()
}

type mfaLoginRequest struct {
	Challenge    string `json:"challenge"`
	Code         string `json:"code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

func (m mfaLoginRequest) Validate() error {
	if m.Challenge == "" || len(m.Challenge) > 128 {
		return errors.New("challenge is required")
	}
	return secondFactor{Code: m.Code, RecoveryCode: m.RecoveryCode}.validate()
}

func totpAAD(user uuid.UUID) []byte { return []byte("users/" + user.String() + "/totp") }

// checkPassword verifies the caller's current password.
func (h *handlers) checkPassword(ctx context.Context, user uuid.UUID, pw string) (auth.Credentials, error) {
	var creds auth.Credentials
	if err := postgres.InReadTx(ctx, h.Pool, postgres.Scope{UserID: user}, func(tx pgx.Tx) error {
		var err error
		creds, err = auth.GetCredentials(ctx, tx, user)
		return err
	}); err != nil {
		return auth.Credentials{}, err
	}
	ok, _, err := h.Hasher.Verify(ctx, pw, creds.PasswordHash)
	if err != nil {
		return auth.Credentials{}, err
	}
	if !ok {
		return auth.Credentials{}, errInvalidPassword
	}
	return creds, nil
}

// verifySecondFactor checks a TOTP code (atomically advancing the replay
// guard) or uses up a recovery code, inside the caller's write transaction.
func (h *handlers) verifySecondFactor(ctx context.Context, tx audit.Tx, user uuid.UUID, f secondFactor) error {
	creds, err := auth.GetCredentials(ctx, tx.Pgx(), user)
	if err != nil {
		return err
	}
	if !creds.TOTPEnabled {
		return errNotEnabled
	}
	if f.RecoveryCode != "" {
		unused, err := auth.UnusedRecoveryCodes(ctx, tx.Pgx(), user)
		if err != nil {
			return err
		}
		i, ok := core.MatchRecoveryCode(f.RecoveryCode, unused)
		if !ok {
			return errInvalidCode
		}
		return auth.UseRecoveryCode(ctx, tx, user, unused[i], h.Now())
	}
	secret, err := h.openSecret(user, creds.TOTPSecretEnc)
	if err != nil {
		return err
	}
	step, ok := core.VerifyTOTP(secret, f.Code, h.Now(), creds.TOTPLastStep)
	if !ok {
		return errInvalidCode
	}
	if err := auth.AdvanceTOTPStep(ctx, tx, user, step); errors.Is(err, auth.ErrTOTPReplay) {
		return errInvalidCode
	} else if err != nil {
		return err
	}
	return nil
}

func (h *handlers) openSecret(user uuid.UUID, enc string) ([]byte, error) {
	s, err := keyring.Decode(enc)
	if err != nil {
		return nil, err
	}
	return h.Keys.Open(s, totpAAD(user))
}

// newRecoveryCodes returns fresh codes and their hashes.
func (h *handlers) newRecoveryCodes() ([]string, [][32]byte, error) {
	raw := make([]byte, core.RecoveryCodeEntropy)
	if _, err := io.ReadFull(h.Rand, raw); err != nil {
		return nil, nil, err
	}
	codes, err := core.RecoveryCodes(raw)
	if err != nil {
		return nil, nil, err
	}
	hashes := make([][32]byte, len(codes))
	for i, c := range codes {
		hashes[i], _ = core.HashRecoveryCode(c)
	}
	return codes, hashes, nil
}

// mfaError maps second-factor failures to responses; it reports whether it
// handled err.
func mfaError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, errInvalidPassword):
		httpx.WriteError(w, r, http.StatusForbidden, "invalid_password", errInvalidPassword.Error())
	case errors.Is(err, errInvalidCode):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_code", errInvalidCode.Error())
	case errors.Is(err, errAlreadyEnabled):
		httpx.WriteError(w, r, http.StatusConflict, "mfa_already_enabled", errAlreadyEnabled.Error())
	case errors.Is(err, errNotEnabled):
		httpx.WriteError(w, r, http.StatusConflict, "mfa_not_enabled", errNotEnabled.Error())
	case errors.Is(err, errNoPendingSetup):
		httpx.WriteError(w, r, http.StatusConflict, "no_pending_setup", errNoPendingSetup.Error())
	default:
		return false
	}
	return true
}

// notify queues a security notice to the user inside tx.
func (h *handlers) notify(ctx context.Context, tx audit.Tx, user uuid.UUID, kind emailcore.Kind) error {
	p, err := auth.GetProfile(ctx, tx.Pgx(), user)
	if err != nil {
		return err
	}
	m, err := emailcore.NewMessage(mail.Address{Name: p.DisplayName, Address: p.Email}, kind, p.Locale, map[string]string{"name": p.DisplayName})
	if err != nil {
		return err
	}
	return h.Mailer.Enqueue(ctx, tx, m)
}

// setupTOTP stores a new pending secret and returns it for the
// authenticator app. It requires the current password.
func (h *handlers) setupTOTP(w http.ResponseWriter, r *http.Request) {
	id, _ := IdentityFrom(r.Context())
	if !h.allow(w, r, h.rules.mfaIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[passwordRequest](w, r, maxBody)
	if !ok {
		return
	}
	creds, err := h.checkPassword(r.Context(), id.UserID, req.Password)
	if err == nil && creds.TOTPEnabled {
		err = errAlreadyEnabled
	}
	if err != nil {
		if !mfaError(w, r, err) {
			h.internalError(w, r, "mfa_setup", err)
		}
		return
	}
	secret := make([]byte, totpSecretBytes)
	if _, err := io.ReadFull(h.Rand, secret); err != nil {
		h.internalError(w, r, "mfa_secret", err)
		return
	}
	sealed, err := h.Keys.Seal(h.Rand, secret, totpAAD(id.UserID))
	if err != nil {
		h.internalError(w, r, "mfa_seal", err)
		return
	}
	var account string
	err = h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: id.UserID}, event("auth.mfa_setup", id.UserID), h.meta(r), func(tx audit.Tx) error {
		p, err := auth.GetProfile(r.Context(), tx.Pgx(), id.UserID)
		if err != nil {
			return err
		}
		account = p.Email
		return auth.SetPendingTOTP(r.Context(), tx, id.UserID, sealed.Encode(), h.Now())
	})
	if err != nil {
		h.internalError(w, r, "mfa_setup", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"secret":      core.EncodeTOTPSecret(secret),
		"otpauth_uri": core.OTPAuthURI(totpIssuer, account, secret),
	})
}

// confirmTOTP turns 2FA on once the user proves their app has the secret,
// returning recovery codes (shown once) and upgrading the session.
func (h *handlers) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	id, _ := IdentityFrom(r.Context())
	if !h.allow(w, r, h.rules.mfaIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[codeRequest](w, r, maxBody)
	if !ok {
		return
	}
	codes, hashes, err := h.newRecoveryCodes()
	if err != nil {
		h.internalError(w, r, "recovery_codes", err)
		return
	}
	err = h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: id.UserID}, event("auth.mfa_enable", id.UserID), h.meta(r), func(tx audit.Tx) error {
		creds, err := auth.GetCredentials(r.Context(), tx.Pgx(), id.UserID)
		switch {
		case err != nil:
			return err
		case creds.TOTPEnabled:
			return errAlreadyEnabled
		case creds.TOTPSecretEnc == "":
			return errNoPendingSetup
		}
		secret, err := h.openSecret(id.UserID, creds.TOTPSecretEnc)
		if err != nil {
			return err
		}
		step, ok := core.VerifyTOTP(secret, req.Code, h.Now(), 0)
		if !ok {
			return errInvalidCode
		}
		if err := auth.EnableTOTP(r.Context(), tx, id.UserID, step, h.Now()); err != nil {
			return err
		}
		if err := auth.ReplaceRecoveryCodes(r.Context(), tx, id.UserID, hashes); err != nil {
			return err
		}
		return h.notify(r.Context(), tx, id.UserID, emailcore.KindMFAEnabled)
	})
	if err != nil {
		if !mfaError(w, r, err) {
			h.internalError(w, r, "mfa_confirm", err)
		}
		return
	}
	if err := h.startSession(w, r, id.UserID, true); err != nil {
		h.internalError(w, r, "session", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// disableTOTP turns 2FA off; it needs the password and a second factor.
func (h *handlers) disableTOTP(w http.ResponseWriter, r *http.Request) {
	h.withPasswordAndSecondFactor(w, r, "auth.mfa_disable", func(tx audit.Tx, id Identity) (any, error) {
		if err := auth.DisableTOTP(r.Context(), tx, id.UserID, h.Now()); err != nil {
			return nil, err
		}
		if err := auth.ReplaceRecoveryCodes(r.Context(), tx, id.UserID, nil); err != nil {
			return nil, err
		}
		return nil, h.notify(r.Context(), tx, id.UserID, emailcore.KindMFADisabled)
	})
}

// regenerateRecoveryCodes replaces all recovery codes.
func (h *handlers) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	codes, hashes, err := h.newRecoveryCodes()
	if err != nil {
		h.internalError(w, r, "recovery_codes", err)
		return
	}
	h.withPasswordAndSecondFactor(w, r, "auth.recovery_codes_regenerate", func(tx audit.Tx, id Identity) (any, error) {
		return map[string]any{"recovery_codes": codes}, auth.ReplaceRecoveryCodes(r.Context(), tx, id.UserID, hashes)
	})
}

// withPasswordAndSecondFactor runs fn in an audited transaction after
// checking the password and a second factor; fn's result (nil for 204) is
// the response.
func (h *handlers) withPasswordAndSecondFactor(w http.ResponseWriter, r *http.Request, action string, fn func(audit.Tx, Identity) (any, error)) {
	id, _ := IdentityFrom(r.Context())
	if !h.allow(w, r, h.rules.mfaIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[secondFactorRequest](w, r, maxBody)
	if !ok {
		return
	}
	if _, err := h.checkPassword(r.Context(), id.UserID, req.Password); err != nil {
		if !mfaError(w, r, err) {
			h.internalError(w, r, action, err)
		}
		return
	}
	var out any
	err := h.Writer.InWriteTx(r.Context(), postgres.Scope{UserID: id.UserID}, event(action, id.UserID), h.meta(r), func(tx audit.Tx) error {
		if err := h.verifySecondFactor(r.Context(), tx, id.UserID, secondFactor{Code: req.Code, RecoveryCode: req.RecoveryCode}); err != nil {
			return err
		}
		var err error
		out, err = fn(tx, id)
		return err
	})
	if err != nil {
		if !mfaError(w, r, err) {
			h.internalError(w, r, action, err)
		}
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, out)
}

// loginMFA completes a login with the second factor.
func (h *handlers) loginMFA(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, h.rules.mfaIP, h.clientIP(r)) {
		return
	}
	req, ok := httpx.Decode[mfaLoginRequest](w, r, maxBody)
	if !ok {
		return
	}
	user, err := h.Challenges.Attempt(r.Context(), req.Challenge)
	if errors.Is(err, auth.ErrChallengeNotFound) || errors.Is(err, auth.ErrChallengeExhausted) {
		httpx.WriteError(w, r, http.StatusUnauthorized, "challenge_invalid", "sign in again")
		return
	}
	if err != nil {
		h.internalError(w, r, "challenge", err)
		return
	}
	factor := secondFactor{Code: req.Code, RecoveryCode: req.RecoveryCode}
	if !h.completeLogin(w, r, auth.LoginRecord{ID: user}, true, func(tx audit.Tx) error {
		return h.verifySecondFactor(r.Context(), tx, user, factor)
	}) {
		return
	}
	if err := h.Challenges.Consume(r.Context(), req.Challenge); err != nil {
		h.Log.WarnContext(r.Context(), "consume challenge failed", "err", err.Error())
	}
}
