package core_test

import (
	"math/rand/v2"
	"slices"
	"testing"
	"testing/quick"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

var now = time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)

const dayDur = 24 * time.Hour

func active(p core.Provider) core.ConnState {
	return core.ConnState{Provider: p, Status: core.StatusActive, LastCheckedAt: now.Add(-time.Hour), KeyID: "k1", ActiveKeyID: "k1"}
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name    string
		s       core.ConnState
		actions []core.Action
		nextDue time.Time
	}{
		{"healthy meta checked an hour ago: nothing until the daily check",
			active(core.ProviderMeta), nil, now.Add(23 * time.Hour)},
		{"meta due for its daily check",
			with(active(core.ProviderMeta), func(s *core.ConnState) { s.LastCheckedAt = now.Add(-25 * time.Hour) }),
			[]core.Action{core.ActionCheck}, now.Add(dayDur)},
		{"never checked",
			with(active(core.ProviderGoogle), func(s *core.ConnState) { s.LastCheckedAt = time.Time{} }),
			[]core.Action{core.ActionCheck}, now.Add(dayDur)},
		{"sealed with an old key: reseal",
			with(active(core.ProviderMeta), func(s *core.ConnState) { s.KeyID = "k0" }),
			[]core.Action{core.ActionReseal}, now.Add(23 * time.Hour)},
		{"linkedin 20 days left: next look when the 14-day warning is due",
			with(active(core.ProviderLinkedIn), func(s *core.ConnState) { s.AccessExpiresAt = now.Add(20 * dayDur) }),
			nil, now.Add(23 * time.Hour)},
		{"linkedin 13 days left: warn at 14",
			with(active(core.ProviderLinkedIn), func(s *core.ConnState) { s.AccessExpiresAt = now.Add(13 * dayDur) }),
			[]core.Action{core.ActionWarn14}, now.Add(23 * time.Hour)},
		{"linkedin 13 days left, already warned at 14",
			with(active(core.ProviderLinkedIn), func(s *core.ConnState) {
				s.AccessExpiresAt = now.Add(13 * dayDur)
				s.WarnedDays = 14
			}),
			nil, now.Add(23 * time.Hour)},
		{"linkedin first seen 2 days out: only the 3-day warning",
			with(active(core.ProviderLinkedIn), func(s *core.ConnState) { s.AccessExpiresAt = now.Add(2 * dayDur) }),
			[]core.Action{core.ActionWarn3}, now.Add(23 * time.Hour)},
		{"linkedin 3-day warning comes due before the daily check",
			with(active(core.ProviderLinkedIn), func(s *core.ConnState) {
				s.AccessExpiresAt = now.Add(3*dayDur + 2*time.Hour)
				s.WarnedDays = 14
			}),
			nil, now.Add(2 * time.Hour)},
		{"linkedin expired without a refresh token: needs re-auth",
			with(active(core.ProviderLinkedIn), func(s *core.ConnState) {
				s.AccessExpiresAt = now.Add(-time.Minute)
				s.WarnedDays = 3
			}),
			[]core.Action{core.ActionMarkReauth}, time.Time{}},
		{"linkedin with a refresh token refreshes 14 days out, no warning",
			with(active(core.ProviderLinkedIn), func(s *core.ConnState) {
				s.AccessExpiresAt = now.Add(10 * dayDur)
				s.HasRefreshToken = true
			}),
			[]core.Action{core.ActionRefresh}, now.Add(23 * time.Hour)},
		{"instagram login token 40 days old: refresh",
			with(active(core.ProviderInstagram), func(s *core.ConnState) {
				s.TokenIssuedAt = now.Add(-40 * dayDur)
				s.AccessExpiresAt = now.Add(20 * dayDur)
			}),
			[]core.Action{core.ActionRefresh}, now.Add(23 * time.Hour)},
		{"instagram login token issued an hour ago with little left: wait for 24h age",
			with(active(core.ProviderInstagram), func(s *core.ConnState) {
				s.TokenIssuedAt = now.Add(-time.Hour)
				s.AccessExpiresAt = now.Add(10 * dayDur)
			}),
			nil, now.Add(23 * time.Hour)},
		{"instagram login token expired: cannot refresh",
			with(active(core.ProviderInstagram), func(s *core.ConnState) {
				s.TokenIssuedAt = now.Add(-61 * dayDur)
				s.AccessExpiresAt = now.Add(-dayDur)
			}),
			[]core.Action{core.ActionMarkReauth}, time.Time{}},
		{"meta data access lapses in 10 days: warn at 14",
			with(active(core.ProviderMeta), func(s *core.ConnState) { s.DataAccessExpiresAt = now.Add(10 * dayDur) }),
			[]core.Action{core.ActionWarn14}, now.Add(23 * time.Hour)},
		{"several at once, in a fixed order",
			with(active(core.ProviderLinkedIn), func(s *core.ConnState) {
				s.KeyID = "k0"
				s.LastCheckedAt = now.Add(-48 * time.Hour)
				s.AccessExpiresAt = now.Add(13 * dayDur)
			}),
			[]core.Action{core.ActionReseal, core.ActionCheck, core.ActionWarn14}, now.Add(dayDur)},
		{"needs re-auth: left alone until the customer reconnects",
			with(active(core.ProviderMeta), func(s *core.ConnState) { s.Status = core.StatusNeedsReauth }),
			nil, time.Time{}},
		{"disconnected: never scheduled",
			with(active(core.ProviderMeta), func(s *core.ConnState) { s.Status = core.StatusDisconnected }),
			nil, time.Time{}},
		{"transient error: keeps its daily check",
			with(active(core.ProviderMeta), func(s *core.ConnState) {
				s.Status = core.StatusError
				s.LastCheckedAt = now.Add(-25 * time.Hour)
			}),
			[]core.Action{core.ActionCheck}, now.Add(dayDur)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := core.Evaluate(tc.s, now)
			if !slices.Equal(plan.Actions, tc.actions) || !plan.NextDue.Equal(tc.nextDue) {
				t.Errorf("Evaluate = %v next %s; want %v next %s", plan.Actions, plan.NextDue, tc.actions, tc.nextDue)
			}
		})
	}
}

func with(s core.ConnState, f func(*core.ConnState)) core.ConnState {
	f(&s)
	return s
}

// Properties over random states: a live connection is always looked at
// again in the future and within a day (the daily check is never lost); a
// parked one (needs re-auth, disconnected, or just marked) is never
// rescheduled; Evaluate is deterministic.
func TestPropertyEvaluateSchedule(t *testing.T) {
	providers := []core.Provider{core.ProviderMeta, core.ProviderInstagram, core.ProviderLinkedIn, core.ProviderGoogle}
	statuses := []core.Status{core.StatusActive, core.StatusError, core.StatusNeedsReauth, core.StatusDisconnected}
	offset := func(r *rand.Rand) time.Time {
		if r.IntN(4) == 0 {
			return time.Time{}
		}
		return now.Add(time.Duration(r.Int64N(int64(120*dayDur))) - 60*dayDur)
	}
	prop := func(seed uint64) bool {
		r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		s := core.ConnState{
			Provider: providers[r.IntN(len(providers))], Status: statuses[r.IntN(len(statuses))],
			AccessExpiresAt: offset(r), DataAccessExpiresAt: offset(r), TokenIssuedAt: offset(r), LastCheckedAt: offset(r),
			HasRefreshToken: r.IntN(2) == 0, WarnedDays: []int{0, 14, 3}[r.IntN(3)],
			KeyID: []string{"", "k0", "k1"}[r.IntN(3)], ActiveKeyID: "k1",
		}
		plan := core.Evaluate(s, now)
		if again := core.Evaluate(s, now); !slices.Equal(again.Actions, plan.Actions) || !again.NextDue.Equal(plan.NextDue) {
			return false
		}
		parked := s.Status == core.StatusNeedsReauth || s.Status == core.StatusDisconnected || slices.Contains(plan.Actions, core.ActionMarkReauth)
		if parked {
			return plan.NextDue.IsZero()
		}
		return plan.NextDue.After(now) && !plan.NextDue.After(now.Add(dayDur))
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 5000}); err != nil {
		t.Fatal(err)
	}
}

// The deadline is when a connection stops working unless a person acts:
// LinkedIn without a refresh token at token expiry; Meta at data-access
// expiry; refreshable providers have none.
func TestDeadline(t *testing.T) {
	exp, data := now.Add(10*dayDur), now.Add(20*dayDur)
	for _, tc := range []struct {
		s    core.ConnState
		want time.Time
	}{
		{core.ConnState{Provider: core.ProviderLinkedIn, AccessExpiresAt: exp}, exp},
		{core.ConnState{Provider: core.ProviderLinkedIn, AccessExpiresAt: exp, HasRefreshToken: true}, time.Time{}},
		{core.ConnState{Provider: core.ProviderMeta, AccessExpiresAt: exp, DataAccessExpiresAt: data}, data},
		{core.ConnState{Provider: core.ProviderInstagram, AccessExpiresAt: exp}, time.Time{}},
		{core.ConnState{Provider: core.ProviderGoogle, AccessExpiresAt: exp}, time.Time{}},
	} {
		if got := tc.s.Deadline(); !got.Equal(tc.want) {
			t.Errorf("%s (refresh %v): Deadline = %s, want %s", tc.s.Provider, tc.s.HasRefreshToken, got, tc.want)
		}
	}
}
