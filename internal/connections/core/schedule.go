package core

import "time"

// Status is a connection's lifecycle state. "Expiring" is not a status: it
// is computed from the deadline when reading (ADR 0009).
type Status string

const (
	StatusActive       Status = "active"
	StatusError        Status = "error" // last maintenance hit a transient failure
	StatusNeedsReauth  Status = "needs_reauth"
	StatusDisconnected Status = "disconnected"
)

// Action is one maintenance step. Evaluate returns them in this order.
type Action string

const (
	ActionReseal     Action = "reseal"      // re-encrypt tokens with the active key
	ActionCheck      Action = "check"       // daily health call (for Google, a refresh)
	ActionRefresh    Action = "refresh"     // renew the access token now
	ActionWarn14     Action = "warn_14"     // email: 14 days before the deadline
	ActionWarn3      Action = "warn_3"      // email: 3 days before the deadline
	ActionMarkReauth Action = "mark_reauth" // the connection can no longer work without the customer
)

const (
	checkEvery          = 24 * time.Hour
	igMinTokenAge       = 24 * time.Hour      // Instagram refuses refreshes of younger tokens
	igRefreshWhenLeft   = 30 * 24 * time.Hour // of a 60-day token
	linkedInRefreshLeft = 14 * 24 * time.Hour
	warnEarly           = 14 * 24 * time.Hour
	warnLate            = 3 * 24 * time.Hour
)

// ConnState is the snapshot of one connection that maintenance decides on.
// Zero times mean "not known" or "not applicable".
type ConnState struct {
	Provider            Provider
	Status              Status
	AccessExpiresAt     time.Time
	DataAccessExpiresAt time.Time // Meta: 90 days after the user was last active
	TokenIssuedAt       time.Time
	LastCheckedAt       time.Time
	HasRefreshToken     bool
	WarnedDays          int // 0 (none yet), 14 or 3: the last warning sent
	KeyID, ActiveKeyID  string
}

// Plan is what to do now, and when to evaluate the connection again (zero:
// not until something else changes it).
type Plan struct {
	Actions []Action
	NextDue time.Time
}

// Evaluate decides a connection's maintenance (ADR 0009). The deadline is
// when the connection stops working unless a person acts: a LinkedIn token
// without a refresh token, or Meta's data-access expiry. Warnings go out 14
// and 3 days before it, once each. A live connection is always due again
// within a day.
func Evaluate(s ConnState, now time.Time) Plan {
	if s.Status == StatusNeedsReauth || s.Status == StatusDisconnected {
		return Plan{}
	}
	deadline := s.deadline()
	igExpired := s.Provider == ProviderInstagram && !s.AccessExpiresAt.IsZero() && !now.Before(s.AccessExpiresAt)
	if igExpired || (!deadline.IsZero() && !now.Before(deadline)) {
		return Plan{Actions: []Action{ActionMarkReauth}}
	}

	var actions []Action
	due := []time.Time{now.Add(checkEvery)}
	if s.KeyID != "" && s.KeyID != s.ActiveKeyID {
		actions = append(actions, ActionReseal)
	}
	if s.LastCheckedAt.IsZero() || !now.Before(s.LastCheckedAt.Add(checkEvery)) {
		actions = append(actions, ActionCheck)
	} else {
		due = append(due, s.LastCheckedAt.Add(checkEvery))
	}
	if at, ok := s.refreshAt(); ok {
		if !now.Before(at) {
			actions = append(actions, ActionRefresh)
		} else {
			due = append(due, at)
		}
	}
	warned := s.WarnedDays
	if !deadline.IsZero() {
		left := deadline.Sub(now)
		switch {
		case left <= warnLate && (warned == 0 || warned > 3):
			actions, warned = append(actions, ActionWarn3), 3
		case left <= warnEarly && warned == 0:
			actions, warned = append(actions, ActionWarn14), 14
		}
		due = append(due, deadline)
		if warned == 0 {
			due = append(due, deadline.Add(-warnEarly))
		}
		if warned == 0 || warned > 3 {
			due = append(due, deadline.Add(-warnLate))
		}
	}
	return Plan{Actions: actions, NextDue: earliestAfter(due, now)}
}

func (s ConnState) deadline() time.Time {
	switch {
	case s.Provider == ProviderLinkedIn && !s.HasRefreshToken:
		return s.AccessExpiresAt
	case s.Provider == ProviderMeta:
		return s.DataAccessExpiresAt
	}
	return time.Time{}
}

// refreshAt is when maintenance should renew the access token, if it does
// for this provider. Google access tokens are renewed on demand by the
// vault and by the daily check instead.
func (s ConnState) refreshAt() (time.Time, bool) {
	switch {
	case s.Provider == ProviderInstagram && !s.TokenIssuedAt.IsZero() && !s.AccessExpiresAt.IsZero():
		return later(s.TokenIssuedAt.Add(igMinTokenAge), s.AccessExpiresAt.Add(-igRefreshWhenLeft)), true
	case s.Provider == ProviderLinkedIn && s.HasRefreshToken && !s.AccessExpiresAt.IsZero():
		return s.AccessExpiresAt.Add(-linkedInRefreshLeft), true
	}
	return time.Time{}, false
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// earliestAfter returns the earliest of ts strictly after now (ts[0] must be
// such a time, so the result is never zero).
func earliestAfter(ts []time.Time, now time.Time) time.Time {
	best := ts[0]
	for _, t := range ts[1:] {
		if t.After(now) && t.Before(best) {
			best = t
		}
	}
	return best
}
