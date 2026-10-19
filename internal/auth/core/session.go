package core

import (
	"time"
	"uuid"
)

// refreshEvery limits LastSeen writes to one per minute per session.
const refreshEvery = time.Minute

// Session is the server-side session state (stored in Redis under the
// hash of the session token).
type Session struct {
	UserID      uuid.UUID `json:"user_id"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeen    time.Time `json:"last_seen"`
	MFAVerified bool      `json:"mfa_verified"`
}

// SessionPolicy bounds session lifetime (ASVS 3.3).
type SessionPolicy struct {
	Idle     time.Duration
	Absolute time.Duration
}

// SessionVerdict is the outcome of checking a session at a moment.
type SessionVerdict struct {
	Valid   bool
	Refresh bool          // store Session (LastSeen moved to now)
	TTL     time.Duration // expiry to set on the stored session
	Session Session
}

// NewSession starts a session at now.
func NewSession(user uuid.UUID, now time.Time, mfaVerified bool) Session {
	return Session{UserID: user, CreatedAt: now, LastSeen: now, MFAVerified: mfaVerified}
}

// EvaluateSession applies p at now. A session idle for p.Idle, older than
// p.Absolute, or with timestamps in the future is invalid.
func EvaluateSession(s Session, p SessionPolicy, now time.Time) SessionVerdict {
	idleFor, age := now.Sub(s.LastSeen), now.Sub(s.CreatedAt)
	if idleFor < 0 || age < 0 || idleFor >= p.Idle || age >= p.Absolute {
		return SessionVerdict{}
	}
	v := SessionVerdict{Valid: true, Session: s}
	if idleFor >= refreshEvery {
		v.Refresh, v.Session.LastSeen, idleFor = true, now, 0
	}
	v.TTL = min(p.Idle-idleFor, p.Absolute-age)
	return v
}

// Account is what a login lookup reveals about an address.
type Account struct {
	Exists        bool
	EmailVerified bool
	TOTPEnabled   bool
	Disabled      bool
}

// LoginOutcome is the result of a password login attempt.
type LoginOutcome int

const (
	LoginInvalid          LoginOutcome = iota // unknown address or wrong password
	LoginDisabled                             // correct password, account disabled
	LoginEmailNotVerified                     // correct password, address unverified
	LoginMFARequired                          // correct password, TOTP challenge next
	LoginSuccess
)

func (o LoginOutcome) String() string {
	return [...]string{"invalid", "disabled", "email_not_verified", "mfa_required", "success"}[o]
}

// DecideLogin maps a lookup and a password check to an outcome. Without the
// correct password every account looks the same.
func DecideLogin(a Account, passwordOK bool) LoginOutcome {
	switch {
	case !a.Exists || !passwordOK:
		return LoginInvalid
	case a.Disabled:
		return LoginDisabled
	case !a.EmailVerified:
		return LoginEmailNotVerified
	case a.TOTPEnabled:
		return LoginMFARequired
	}
	return LoginSuccess
}
