package core_test

import (
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/ratelimit/core"
)

var t0 = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) // a window boundary for 1m windows

func limit(t *testing.T, max int, window time.Duration) core.Limit {
	t.Helper()
	l, err := core.NewLimit(max, window)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestNewLimitValidates(t *testing.T) {
	for _, tc := range []struct {
		max    int
		window time.Duration
	}{{0, time.Minute}, {-1, time.Minute}, {5, 0}, {5, -time.Second}, {5, 500 * time.Millisecond}} {
		if _, err := core.NewLimit(tc.max, tc.window); !errors.Is(err, core.ErrInvalidLimit) {
			t.Errorf("NewLimit(%d, %v) err = %v", tc.max, tc.window, err)
		}
	}
}

func TestWindowStart(t *testing.T) {
	if got := core.WindowStart(t0.Add(59*time.Second), time.Minute); !got.Equal(t0) {
		t.Errorf("WindowStart = %v", got)
	}
	if got := core.WindowStart(t0.Add(time.Minute), time.Minute); !got.Equal(t0.Add(time.Minute)) {
		t.Errorf("WindowStart at boundary = %v", got)
	}
}

func TestDecide(t *testing.T) {
	l := limit(t, 10, time.Minute)
	cases := []struct {
		name      string
		counts    core.Counts
		at        time.Duration // offset into the current window
		allowed   bool
		remaining int
		retry     time.Duration
	}{
		{"fresh subject", core.Counts{}, 0, true, 9, 0},
		{"last allowed request", core.Counts{Curr: 9}, 30 * time.Second, true, 0, 0},
		{"over the limit in this window", core.Counts{Curr: 10}, 15 * time.Second, false, 0, 45*time.Second + 6*time.Second},
		{"previous window still weighs at the start", core.Counts{Prev: 10}, 0, false, 0, 6 * time.Second},
		{"previous window half decayed", core.Counts{Prev: 10, Curr: 4}, 30 * time.Second, true, 0, 0},
		{"previous window half decayed, one too many", core.Counts{Prev: 10, Curr: 5}, 30 * time.Second, false, 0, 6 * time.Second},
		{"previous window fully decayed at the end", core.Counts{Prev: 1000}, 59*time.Second + 999*time.Millisecond, true, 8, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := core.Decide(l, tc.counts, t0.Add(tc.at))
			if d.Allowed != tc.allowed || d.Remaining != tc.remaining {
				t.Errorf("Decide = %+v, want allowed=%v remaining=%d", d, tc.allowed, tc.remaining)
			}
			if !tc.allowed && (d.RetryAfter < tc.retry-time.Millisecond || d.RetryAfter > tc.retry+time.Millisecond) {
				t.Errorf("RetryAfter = %v, want ≈%v", d.RetryAfter, tc.retry)
			}
			if tc.allowed && d.RetryAfter != 0 {
				t.Errorf("RetryAfter = %v on an allowed request", d.RetryAfter)
			}
		})
	}
}

// Property: in a quiet previous window, exactly Max requests pass per window.
func TestPropertyExactlyMaxPerFreshWindow(t *testing.T) {
	prop := func(max uint8, offsets []uint16) bool {
		l, err := core.NewLimit(int(max%50)+1, time.Minute)
		if err != nil {
			return false
		}
		allowed := 0
		var curr int64
		for i := range l.Max() + 5 {
			off := time.Duration(0)
			if i < len(offsets) {
				off = time.Duration(offsets[i]%60000) * time.Millisecond
			}
			if core.Decide(l, core.Counts{Curr: curr}, t0.Add(off)).Allowed {
				allowed++
			}
			curr++ // attempts count even when rejected
		}
		return allowed == l.Max()
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

// Property: more prior traffic never makes a request more likely to pass,
// and waiting RetryAfter is enough for the request to pass.
func TestPropertyMonotoneAndRetryAfterSuffices(t *testing.T) {
	l := limit(t, 20, time.Minute)
	prop := func(prev, curr, extra uint8, atMs uint16) bool {
		at := t0.Add(time.Duration(atMs%60000) * time.Millisecond)
		c := core.Counts{Prev: int64(prev % 40), Curr: int64(curr % 40)}
		more := core.Counts{Prev: c.Prev + int64(extra%5), Curr: c.Curr + int64(extra%3)}
		d := core.Decide(l, c, at)
		if core.Decide(l, more, at).Allowed && !d.Allowed {
			return false
		}
		if d.Allowed {
			return d.RetryAfter == 0
		}
		// Age the counters to the retry instant, assuming no new traffic.
		later := at.Add(d.RetryAfter)
		aged := c
		switch shift := int(core.WindowStart(later, time.Minute).Sub(t0) / time.Minute); {
		case shift == 1:
			aged = core.Counts{Prev: c.Curr}
		case shift >= 2:
			aged = core.Counts{}
		}
		return d.RetryAfter > 0 && core.Decide(l, aged, later).Allowed
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 5000}); err != nil {
		t.Error(err)
	}
}

func BenchmarkDecide(b *testing.B) {
	l, _ := core.NewLimit(120, time.Minute)
	c := core.Counts{Prev: 80, Curr: 40}
	now := t0.Add(17 * time.Second)
	b.ReportAllocs()
	for b.Loop() {
		_ = core.Decide(l, c, now)
	}
}
