// Package core is the pure sliding-window-counter rate limiting rule.
//
// Requests are counted in fixed windows. The estimate for "the last Window"
// is the current window's count plus the previous window's count weighted
// by how much of it still overlaps the sliding window. Callers pass counts
// observed before the request being decided; attempts are counted even when
// rejected, so a client that keeps hammering stays blocked.
package core

import (
	"errors"
	"math"
	"time"
)

// ErrInvalidLimit reports a limit that cannot be enforced.
var ErrInvalidLimit = errors.New("ratelimit: limit needs max >= 1 and a window of at least 1s")

// Limit allows Max requests per sliding Window.
type Limit struct {
	max    int
	window time.Duration
}

// NewLimit validates and returns a Limit.
func NewLimit(maxRequests int, window time.Duration) (Limit, error) {
	if maxRequests < 1 || window < time.Second {
		return Limit{}, ErrInvalidLimit
	}
	return Limit{max: maxRequests, window: window}, nil
}

func (l Limit) Max() int              { return l.max }
func (l Limit) Window() time.Duration { return l.window }

// Counts are the requests seen in the previous and the current fixed window.
type Counts struct {
	Prev, Curr int64
}

// Decision is the outcome for one request.
type Decision struct {
	Allowed    bool
	Remaining  int           // requests still allowed after this one
	RetryAfter time.Duration // zero when allowed
}

// WindowStart is the start of the fixed window containing now.
func WindowStart(now time.Time, window time.Duration) time.Time { return now.Truncate(window) }

// Decide applies l to a request at now, given the counts before it.
func Decide(l Limit, c Counts, now time.Time) Decision {
	w := float64(l.window)
	elapsed := float64(now.Sub(WindowStart(now, l.window)))
	limit := float64(l.max)
	estimate := float64(c.Prev)*(1-elapsed/w) + float64(c.Curr)

	if estimate+1 <= limit {
		return Decision{Allowed: true, Remaining: int(math.Floor(limit - estimate - 1))}
	}
	var wait float64
	if float64(c.Curr)+1 <= limit {
		// The previous window must decay until prev*(1-(elapsed+t)/w)+curr+1 <= max.
		wait = w*(1-(limit-1-float64(c.Curr))/float64(c.Prev)) - elapsed
	} else {
		// Only the next window helps: the current count becomes "previous"
		// and must decay until curr*(1-s/w)+1 <= max.
		wait = (w - elapsed) + w*(1-(limit-1)/float64(c.Curr))
	}
	return Decision{RetryAfter: ceilMillis(wait)}
}

// ceilMillis rounds a duration in nanoseconds up to the next millisecond,
// never below 1ms, so waiting RetryAfter is always sufficient.
func ceilMillis(ns float64) time.Duration {
	ms := math.Ceil(ns / float64(time.Millisecond))
	return time.Duration(math.Max(ms, 1)) * time.Millisecond
}
