package core_test

import (
	"testing"
	"testing/quick"
	"time"
	"uuid"

	"github.com/yahya31415/tuorps/internal/auth/core"
)

func policy() core.SessionPolicy { return core.SessionPolicy{Idle: 12 * time.Hour, Absolute: 14 * 24 * time.Hour} }

func t0() time.Time { return time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC) }

func TestNewSession(t *testing.T) {
	u := uuid.MustParse("0199c3f0-0000-7000-8000-0000000000a1")
	s := core.NewSession(u, t0(), true)
	if s.UserID != u || !s.CreatedAt.Equal(t0()) || !s.LastSeen.Equal(t0()) || !s.MFAVerified {
		t.Fatalf("NewSession = %+v", s)
	}
}

func TestEvaluateSession(t *testing.T) {
	s := core.NewSession(uuid.MustParse("0199c3f0-0000-7000-8000-0000000000a1"), t0(), false)
	cases := []struct {
		name     string
		lastSeen time.Duration // after creation
		now      time.Duration // after creation
		valid    bool
		refresh  bool
		ttl      time.Duration
	}{
		{"fresh", 0, 0, true, false, 12 * time.Hour},
		{"used recently: no refresh write", 0, 30 * time.Second, true, false, 12*time.Hour - 30*time.Second},
		{"active: refresh last seen", 0, 2 * time.Hour, true, true, 12 * time.Hour},
		{"idle too long", 0, 12*time.Hour + time.Second, false, false, 0},
		{"near the absolute limit: TTL shrinks", 13*24*time.Hour + 20*time.Hour, 13*24*time.Hour + 21*time.Hour, true, true, 3 * time.Hour},
		{"past the absolute limit", 14*24*time.Hour - time.Minute, 14*24*time.Hour + time.Second, false, false, 0},
		{"last seen in the future (tampered)", 2 * time.Hour, time.Hour, false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := s
			in.LastSeen = t0().Add(tc.lastSeen)
			v := core.EvaluateSession(in, policy(), t0().Add(tc.now))
			if v.Valid != tc.valid || v.Refresh != tc.refresh || v.TTL != tc.ttl {
				t.Fatalf("EvaluateSession = %+v, want valid=%v refresh=%v ttl=%v", v, tc.valid, tc.refresh, tc.ttl)
			}
			if v.Refresh && !v.Session.LastSeen.Equal(t0().Add(tc.now)) {
				t.Errorf("refreshed LastSeen = %v", v.Session.LastSeen)
			}
			if in.LastSeen != t0().Add(tc.lastSeen) {
				t.Error("input session mutated")
			}
		})
	}
}

// Property: a valid session's TTL never outlives either limit.
func TestPropertySessionTTLBounded(t *testing.T) {
	prop := func(createdAgo, seenAgo uint32) bool {
		now := t0().Add(30 * 24 * time.Hour)
		created := now.Add(-time.Duration(createdAgo%(20*24*3600)) * time.Second)
		seen := created.Add(time.Duration(seenAgo%(20*24*3600)) * time.Second)
		if seen.After(now) {
			seen = now
		}
		s := core.Session{CreatedAt: created, LastSeen: seen}
		v := core.EvaluateSession(s, policy(), now)
		if !v.Valid {
			return v.TTL == 0
		}
		return v.TTL > 0 && v.TTL <= policy().Idle && now.Add(v.TTL).Sub(created) <= policy().Absolute
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

func TestDecideLogin(t *testing.T) {
	ok := core.Account{Exists: true, EmailVerified: true}
	cases := []struct {
		name string
		a    core.Account
		pwOK bool
		want core.LoginOutcome
	}{
		{"unknown user", core.Account{}, false, core.LoginInvalid},
		{"wrong password", ok, false, core.LoginInvalid},
		{"success", ok, true, core.LoginSuccess},
		{"two-factor required", core.Account{Exists: true, EmailVerified: true, TOTPEnabled: true}, true, core.LoginMFARequired},
		{"email not verified", core.Account{Exists: true}, true, core.LoginEmailNotVerified},
		{"disabled", core.Account{Exists: true, EmailVerified: true, Disabled: true}, true, core.LoginDisabled},
		{"disabled with wrong password reveals nothing", core.Account{Exists: true, Disabled: true}, false, core.LoginInvalid},
		{"unverified with wrong password reveals nothing", core.Account{Exists: true}, false, core.LoginInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := core.DecideLogin(tc.a, tc.pwOK); got != tc.want {
				t.Fatalf("DecideLogin = %v, want %v", got, tc.want)
			}
		})
	}
}

// Property: only a correct password for an existing, verified, enabled
// account can produce anything other than LoginInvalid... and only success
// or the MFA challenge grant progress.
func TestPropertyLoginNeverLeaksWithoutThePassword(t *testing.T) {
	prop := func(exists, verified, totp, disabled bool) bool {
		a := core.Account{Exists: exists, EmailVerified: verified, TOTPEnabled: totp, Disabled: disabled}
		if core.DecideLogin(a, false) != core.LoginInvalid {
			return false
		}
		out := core.DecideLogin(a, true)
		progress := out == core.LoginSuccess || out == core.LoginMFARequired
		return progress == (exists && verified && !disabled) && (out == core.LoginMFARequired) == (progress && totp)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}
