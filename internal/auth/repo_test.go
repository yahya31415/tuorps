package auth_test

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/auth"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

func appPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pg := testinfra.MigratedPostgres(t)
	p, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(pg.AppURL), MaxConns: 4, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func ev(t *testing.T, action string, target uuid.UUID) auditcore.Event {
	t.Helper()
	e, err := auditcore.NewEvent(action, "user", target.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func hash(s string) [32]byte { return sha256.Sum256([]byte(s)) }

func TestUsersRepository(t *testing.T) {
	pool := appPool(t)
	w := audit.NewWriter(pool, ids.System(), clock.System())
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	alice, bob := uuid.MustParse("0199c3f0-0000-7000-8000-0000000000a1"), uuid.MustParse("0199c3f0-0000-7000-8000-0000000000b1")
	self := func(u uuid.UUID) postgres.Scope { return postgres.Scope{UserID: u} }

	for _, a := range []auth.NewAccount{
		{ID: alice, Email: "Priya@Example.in", EmailNormalized: "priya@example.in", DisplayName: "Priya", Locale: "en", PasswordHash: "$argon2id$alice", Now: now},
		{ID: bob, Email: "arjun@example.in", EmailNormalized: "arjun@example.in", DisplayName: "Arjun", Locale: "ml", PasswordHash: "$argon2id$bob", Now: now},
	} {
		if err := w.InWriteTx(t.Context(), self(a.ID), ev(t, "auth.signup", a.ID), auditcore.Meta{}, func(tx audit.Tx) error {
			return auth.CreateAccount(t.Context(), tx, a)
		}); err != nil {
			t.Fatalf("CreateAccount(%s): %v", a.Email, err)
		}
	}

	read := func(s postgres.Scope, fn func(pgx.Tx) error) {
		t.Helper()
		if err := postgres.InReadTx(t.Context(), pool, s, fn); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("duplicate addresses are reported, not overwritten", func(t *testing.T) {
		carol := uuid.MustParse("0199c3f0-0000-7000-8000-0000000000c1")
		err := w.InWriteTx(t.Context(), self(carol), ev(t, "auth.signup", carol), auditcore.Meta{}, func(tx audit.Tx) error {
			return auth.CreateAccount(t.Context(), tx, auth.NewAccount{ID: carol, Email: "priya@example.in", EmailNormalized: "priya@example.in", DisplayName: "Impostor", Locale: "en", PasswordHash: "x", Now: now})
		})
		if !errors.Is(err, auth.ErrEmailTaken) {
			t.Fatalf("err = %v, want ErrEmailTaken", err)
		}
	})

	t.Run("login lookup works without a tenant scope", func(t *testing.T) {
		read(postgres.Scope{}, func(tx pgx.Tx) error {
			rec, ok, err := auth.LookupLogin(t.Context(), tx, "priya@example.in")
			if err != nil || !ok || rec.ID != alice || rec.PasswordHash != "$argon2id$alice" || rec.Account.EmailVerified || !rec.Account.Exists {
				t.Errorf("LookupLogin = %+v %v %v", rec, ok, err)
			}
			if _, ok, _ := auth.LookupLogin(t.Context(), tx, "nobody@example.in"); ok {
				t.Error("unknown email found")
			}
			return nil
		})
	})

	t.Run("profile and credentials are visible to their owner only", func(t *testing.T) {
		read(self(alice), func(tx pgx.Tx) error {
			p, err := auth.GetProfile(t.Context(), tx, alice)
			if err != nil || p.Email != "Priya@Example.in" || p.DisplayName != "Priya" || p.EmailVerified {
				t.Errorf("own profile = %+v, %v", p, err)
			}
			c, err := auth.GetCredentials(t.Context(), tx, alice)
			if err != nil || c.PasswordHash != "$argon2id$alice" || c.TOTPEnabled {
				t.Errorf("own credentials = %+v, %v", c, err)
			}
			if _, err := auth.GetProfile(t.Context(), tx, bob); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("other's profile: err = %v", err)
			}
			if _, err := auth.GetCredentials(t.Context(), tx, bob); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("other's credentials: err = %v", err)
			}
			return nil
		})
	})

	t.Run("writes to another user's account affect nothing", func(t *testing.T) {
		err := w.InWriteTx(t.Context(), self(alice), ev(t, "auth.password_change", bob), auditcore.Meta{}, func(tx audit.Tx) error {
			return auth.SetPasswordHash(t.Context(), tx, bob, "$argon2id$pwned", now)
		})
		if !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		read(postgres.Scope{}, func(tx pgx.Tx) error {
			rec, _, _ := auth.LookupLogin(t.Context(), tx, "arjun@example.in")
			if rec.PasswordHash != "$argon2id$bob" {
				t.Errorf("bob's hash changed to %q", rec.PasswordHash)
			}
			return nil
		})
	})

	t.Run("profile and password updates", func(t *testing.T) {
		err := w.InWriteTx(t.Context(), self(alice), ev(t, "auth.profile_update", alice), auditcore.Meta{}, func(tx audit.Tx) error {
			if err := auth.UpdateProfile(t.Context(), tx, alice, "Priya S", "hi", now); err != nil {
				return err
			}
			return auth.SetPasswordHash(t.Context(), tx, alice, "$argon2id$alice2", now)
		})
		if err != nil {
			t.Fatal(err)
		}
		read(self(alice), func(tx pgx.Tx) error {
			p, _ := auth.GetProfile(t.Context(), tx, alice)
			c, _ := auth.GetCredentials(t.Context(), tx, alice)
			if p.DisplayName != "Priya S" || p.Locale != "hi" || c.PasswordHash != "$argon2id$alice2" {
				t.Errorf("after update: %+v %+v", p, c)
			}
			return nil
		})
	})

	t.Run("email tokens: issue supersedes, redeem once, then verify", func(t *testing.T) {
		issue := func(id, token string) {
			t.Helper()
			err := w.InWriteTx(t.Context(), self(alice), ev(t, "auth.verification_sent", alice), auditcore.Meta{}, func(tx audit.Tx) error {
				return auth.IssueEmailToken(t.Context(), tx, uuid.MustParse(id), alice, auth.PurposeVerifyEmail, hash(token), now.Add(24*time.Hour), now)
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		issue("0199c3f0-0000-7000-8000-0000000002a1", "first")
		issue("0199c3f0-0000-7000-8000-0000000002a2", "second")
		redeem := func(token string, purpose auth.TokenPurpose) (uuid.UUID, bool) {
			var u uuid.UUID
			var ok bool
			err := postgres.InTx(t.Context(), pool, postgres.Scope{}, func(tx pgx.Tx) error {
				var err error
				u, ok, err = auth.RedeemEmailToken(t.Context(), tx, hash(token), purpose, now.Add(time.Minute))
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return u, ok
		}
		if _, ok := redeem("first", auth.PurposeVerifyEmail); ok {
			t.Error("superseded token redeemed")
		}
		if _, ok := redeem("second", auth.PurposeResetPassword); ok {
			t.Error("token redeemed for the wrong purpose")
		}
		if u, ok := redeem("second", auth.PurposeVerifyEmail); !ok || u != alice {
			t.Errorf("redeem = %v %v", u, ok)
		}
		if _, ok := redeem("second", auth.PurposeVerifyEmail); ok {
			t.Error("token redeemed twice")
		}
		err := w.InWriteTx(t.Context(), self(alice), ev(t, "auth.email_verified", alice), auditcore.Meta{}, func(tx audit.Tx) error {
			return auth.MarkEmailVerified(t.Context(), tx, alice, now)
		})
		if err != nil {
			t.Fatal(err)
		}
		read(postgres.Scope{}, func(tx pgx.Tx) error {
			rec, _, _ := auth.LookupLogin(t.Context(), tx, "priya@example.in")
			if !rec.Account.EmailVerified {
				t.Error("email not marked verified")
			}
			return nil
		})
	})

	t.Run("TOTP lifecycle with an atomic replay guard", func(t *testing.T) {
		write := func(action string, fn func(audit.Tx) error) error {
			return w.InWriteTx(t.Context(), self(alice), ev(t, action, alice), auditcore.Meta{}, fn)
		}
		if err := write("auth.mfa_setup", func(tx audit.Tx) error {
			return auth.SetPendingTOTP(t.Context(), tx, alice, "v1.k1.c2VjcmV0", now)
		}); err != nil {
			t.Fatal(err)
		}
		read(self(alice), func(tx pgx.Tx) error {
			c, _ := auth.GetCredentials(t.Context(), tx, alice)
			if c.TOTPSecretEnc != "v1.k1.c2VjcmV0" || c.TOTPEnabled {
				t.Errorf("pending TOTP = %+v", c)
			}
			return nil
		})
		if err := write("auth.mfa_enable", func(tx audit.Tx) error { return auth.EnableTOTP(t.Context(), tx, alice, 100, now) }); err != nil {
			t.Fatal(err)
		}
		advance := func(step int64) error {
			return write("auth.mfa_use", func(tx audit.Tx) error { return auth.AdvanceTOTPStep(t.Context(), tx, alice, step) })
		}
		if err := advance(100); !errors.Is(err, auth.ErrTOTPReplay) {
			t.Errorf("same step again: err = %v, want ErrTOTPReplay", err)
		}
		if err := advance(101); err != nil {
			t.Errorf("next step: %v", err)
		}
		read(postgres.Scope{}, func(tx pgx.Tx) error {
			rec, _, _ := auth.LookupLogin(t.Context(), tx, "priya@example.in")
			if !rec.Account.TOTPEnabled {
				t.Error("TOTP not enabled")
			}
			return nil
		})
		if err := write("auth.mfa_disable", func(tx audit.Tx) error { return auth.DisableTOTP(t.Context(), tx, alice, now) }); err != nil {
			t.Fatal(err)
		}
		read(self(alice), func(tx pgx.Tx) error {
			c, _ := auth.GetCredentials(t.Context(), tx, alice)
			if c.TOTPEnabled || c.TOTPSecretEnc != "" || c.TOTPLastStep != 0 {
				t.Errorf("after disable = %+v", c)
			}
			return nil
		})
	})

	t.Run("recovery codes are replaced wholesale and used once", func(t *testing.T) {
		codes := [][32]byte{hash("c1"), hash("c2"), hash("c3")}
		write := func(fn func(audit.Tx) error) error {
			return w.InWriteTx(t.Context(), self(alice), ev(t, "auth.recovery_codes", alice), auditcore.Meta{}, fn)
		}
		if err := write(func(tx audit.Tx) error { return auth.ReplaceRecoveryCodes(t.Context(), tx, alice, codes) }); err != nil {
			t.Fatal(err)
		}
		if err := write(func(tx audit.Tx) error { return auth.UseRecoveryCode(t.Context(), tx, alice, hash("c2"), now) }); err != nil {
			t.Fatal(err)
		}
		if err := write(func(tx audit.Tx) error { return auth.UseRecoveryCode(t.Context(), tx, alice, hash("c2"), now) }); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("reuse: err = %v", err)
		}
		read(self(alice), func(tx pgx.Tx) error {
			left, err := auth.UnusedRecoveryCodes(t.Context(), tx, alice)
			if err != nil || len(left) != 2 {
				t.Errorf("unused = %d, %v", len(left), err)
			}
			return nil
		})
		read(self(bob), func(tx pgx.Tx) error {
			if left, _ := auth.UnusedRecoveryCodes(t.Context(), tx, alice); len(left) != 0 {
				t.Errorf("bob sees %d of alice's codes", len(left))
			}
			return nil
		})
		if err := write(func(tx audit.Tx) error { return auth.ReplaceRecoveryCodes(t.Context(), tx, alice, codes[:1]) }); err != nil {
			t.Fatal(err)
		}
		read(self(alice), func(tx pgx.Tx) error {
			if left, _ := auth.UnusedRecoveryCodes(t.Context(), tx, alice); len(left) != 1 {
				t.Errorf("after replace: %d codes", len(left))
			}
			return nil
		})
	})
}
