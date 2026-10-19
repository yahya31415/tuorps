package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/auth/adapters/postgres/sqlcgen"
	"github.com/yahya31415/tuorps/internal/auth/core"
)

// Repository errors. ErrNotFound also covers rows hidden by row-level
// security, so callers cannot tell "absent" from "someone else's".
var (
	ErrNotFound   = errors.New("auth: not found")
	ErrEmailTaken = errors.New("auth: email address already registered")
	ErrTOTPReplay = errors.New("auth: TOTP step already used")
)

// TokenPurpose is what an email token authorises.
type TokenPurpose string

const (
	PurposeVerifyEmail   TokenPurpose = "verify_email"
	PurposeResetPassword TokenPurpose = "reset_password"
)

// NewAccount is a user to create.
type NewAccount struct {
	ID              uuid.UUID
	Email           string
	EmailNormalized string
	DisplayName     string
	Locale          string
	PasswordHash    string
	Now             time.Time
}

// LoginRecord is what the pre-authentication lookup returns.
type LoginRecord struct {
	ID           uuid.UUID
	Email        string
	DisplayName  string
	Locale       string
	PasswordHash string
	Account      core.Account
}

// Profile is a user's public profile.
type Profile struct {
	ID            uuid.UUID
	Email         string
	DisplayName   string
	Locale        string
	EmailVerified bool
	CreatedAt     time.Time
}

// Credentials are visible only to their owner.
type Credentials struct {
	PasswordHash  string
	TOTPSecretEnc string
	TOTPEnabled   bool
	TOTPLastStep  int64
}

func q(tx pgx.Tx) *sqlcgen.Queries { return sqlcgen.New(tx) }

func affected(n int64, err error, none error) error {
	if err != nil {
		return err
	}
	if n == 0 {
		return none
	}
	return nil
}

// CreateAccount inserts a user and their credentials. The scope's user must
// be a.ID (row-level security).
func CreateAccount(ctx context.Context, tx audit.Tx, a NewAccount) error {
	n, err := q(tx.Pgx()).InsertUser(ctx, sqlcgen.InsertUserParams{
		ID: a.ID, Email: a.Email, EmailNormalized: a.EmailNormalized, DisplayName: a.DisplayName, Locale: a.Locale, Now: a.Now,
	})
	if err := affected(n, err, ErrEmailTaken); err != nil {
		return err
	}
	return q(tx.Pgx()).InsertCredentials(ctx, sqlcgen.InsertCredentialsParams{UserID: a.ID, PasswordHash: a.PasswordHash, Now: a.Now})
}

// LookupLogin finds an account by normalised email without a tenant scope.
func LookupLogin(ctx context.Context, tx pgx.Tx, emailNormalized string) (LoginRecord, bool, error) {
	r, err := q(tx).LookupLogin(ctx, emailNormalized)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginRecord{}, false, nil
	}
	if err != nil {
		return LoginRecord{}, false, fmt.Errorf("auth: lookup: %w", err)
	}
	return LoginRecord{
		ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, Locale: r.Locale, PasswordHash: r.PasswordHash,
		Account: core.Account{Exists: true, EmailVerified: r.EmailVerified, TOTPEnabled: r.TotpEnabled, Disabled: r.Disabled},
	}, true, nil
}

// GetProfile returns a visible user's profile.
func GetProfile(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Profile, error) {
	r, err := q(tx).GetProfile(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	return Profile{ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, Locale: r.Locale, EmailVerified: r.EmailVerifiedAt != nil, CreatedAt: r.CreatedAt}, nil
}

// GetCredentials returns the scoped user's own credentials.
func GetCredentials(ctx context.Context, tx pgx.Tx, user uuid.UUID) (Credentials, error) {
	r, err := q(tx).GetCredentials(ctx, user)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credentials{}, ErrNotFound
	}
	if err != nil {
		return Credentials{}, err
	}
	c := Credentials{PasswordHash: r.PasswordHash, TOTPEnabled: r.TotpEnabledAt != nil}
	if r.TotpSecretEnc != nil {
		c.TOTPSecretEnc = *r.TotpSecretEnc
	}
	if r.TotpLastStep != nil {
		c.TOTPLastStep = *r.TotpLastStep
	}
	return c, nil
}

// UpdateProfile changes the display name and locale.
func UpdateProfile(ctx context.Context, tx audit.Tx, id uuid.UUID, name, locale string, now time.Time) error {
	n, err := q(tx.Pgx()).UpdateProfile(ctx, sqlcgen.UpdateProfileParams{ID: id, DisplayName: name, Locale: locale, Now: now})
	return affected(n, err, ErrNotFound)
}

// SetPasswordHash replaces the user's password hash.
func SetPasswordHash(ctx context.Context, tx audit.Tx, user uuid.UUID, hash string, now time.Time) error {
	n, err := q(tx.Pgx()).SetPasswordHash(ctx, sqlcgen.SetPasswordHashParams{UserID: user, PasswordHash: hash, Now: now})
	return affected(n, err, ErrNotFound)
}

// MarkEmailVerified records that the user proved control of their address.
func MarkEmailVerified(ctx context.Context, tx audit.Tx, user uuid.UUID, now time.Time) error {
	n, err := q(tx.Pgx()).MarkEmailVerified(ctx, sqlcgen.MarkEmailVerifiedParams{ID: user, Now: now})
	return affected(n, err, ErrNotFound)
}

// IssueEmailToken stores a token hash, superseding unused tokens of the same purpose.
func IssueEmailToken(ctx context.Context, tx audit.Tx, id, user uuid.UUID, purpose TokenPurpose, hash [32]byte, expires, now time.Time) error {
	if err := q(tx.Pgx()).SupersedeEmailTokens(ctx, sqlcgen.SupersedeEmailTokensParams{UserID: user, Purpose: string(purpose), Now: &now}); err != nil {
		return err
	}
	return q(tx.Pgx()).InsertEmailToken(ctx, sqlcgen.InsertEmailTokenParams{
		ID: id, UserID: user, Purpose: string(purpose), TokenHash: hash[:], ExpiresAt: expires, Now: now,
	})
}

// RedeemEmailToken atomically uses a token and returns its user.
func RedeemEmailToken(ctx context.Context, tx pgx.Tx, hash [32]byte, purpose TokenPurpose, now time.Time) (uuid.UUID, bool, error) {
	u, err := q(tx).RedeemEmailToken(ctx, sqlcgen.RedeemEmailTokenParams{TokenHash: hash[:], Purpose: string(purpose), Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil(), false, nil
	}
	if err != nil {
		return uuid.Nil(), false, err
	}
	return u, true, nil
}

// SetPendingTOTP stores an encrypted secret awaiting confirmation.
func SetPendingTOTP(ctx context.Context, tx audit.Tx, user uuid.UUID, secretEnc string, now time.Time) error {
	n, err := q(tx.Pgx()).SetPendingTOTP(ctx, sqlcgen.SetPendingTOTPParams{UserID: user, SecretEnc: &secretEnc, Now: now})
	return affected(n, err, ErrNotFound)
}

// EnableTOTP turns on the pending secret, recording the confirming step.
func EnableTOTP(ctx context.Context, tx audit.Tx, user uuid.UUID, step int64, now time.Time) error {
	n, err := q(tx.Pgx()).EnableTOTP(ctx, sqlcgen.EnableTOTPParams{UserID: user, Step: step, Now: &now})
	return affected(n, err, ErrNotFound)
}

// AdvanceTOTPStep records a used step; an old or repeated step is ErrTOTPReplay.
func AdvanceTOTPStep(ctx context.Context, tx audit.Tx, user uuid.UUID, step int64) error {
	n, err := q(tx.Pgx()).AdvanceTOTPStep(ctx, sqlcgen.AdvanceTOTPStepParams{UserID: user, Step: step})
	return affected(n, err, ErrTOTPReplay)
}

// DisableTOTP removes the secret.
func DisableTOTP(ctx context.Context, tx audit.Tx, user uuid.UUID, now time.Time) error {
	n, err := q(tx.Pgx()).DisableTOTP(ctx, sqlcgen.DisableTOTPParams{UserID: user, Now: now})
	return affected(n, err, ErrNotFound)
}

// ReplaceRecoveryCodes swaps the user's codes for the given hashes.
func ReplaceRecoveryCodes(ctx context.Context, tx audit.Tx, user uuid.UUID, hashes [][32]byte) error {
	if err := q(tx.Pgx()).DeleteRecoveryCodes(ctx, user); err != nil {
		return err
	}
	for _, h := range hashes {
		if err := q(tx.Pgx()).InsertRecoveryCode(ctx, sqlcgen.InsertRecoveryCodeParams{UserID: user, CodeHash: h[:]}); err != nil {
			return err
		}
	}
	return nil
}

// UnusedRecoveryCodes returns the hashes of the user's remaining codes.
func UnusedRecoveryCodes(ctx context.Context, tx pgx.Tx, user uuid.UUID) ([][32]byte, error) {
	rows, err := q(tx).UnusedRecoveryCodes(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([][32]byte, 0, len(rows))
	for _, r := range rows {
		var h [32]byte
		copy(h[:], r)
		out = append(out, h)
	}
	return out, nil
}

// UseRecoveryCode marks one code used; a used or unknown code is ErrNotFound.
func UseRecoveryCode(ctx context.Context, tx audit.Tx, user uuid.UUID, hash [32]byte, now time.Time) error {
	n, err := q(tx.Pgx()).UseRecoveryCode(ctx, sqlcgen.UseRecoveryCodeParams{UserID: user, CodeHash: hash[:], Now: &now})
	return affected(n, err, ErrNotFound)
}
