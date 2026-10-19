// Package ratelimit enforces per-subject request limits (CLAUDE.md §3: auth,
// webhook and write routes). Counting is shared through Redis; the decision
// itself is the pure rule in ratelimit/core.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit/core"
)

const keyPrefix = "tuorps:v1:rl:"

// ErrUnavailable means the counter store failed for a fail-closed rule.
var ErrUnavailable = errors.New("ratelimit: limiter unavailable")

// Store records one attempt for key in the fixed window containing now and
// returns the counts as they were before it.
type Store func(ctx context.Context, key string, window time.Duration, now time.Time) (core.Counts, error)

// Rule is a named limit. FailClosed rules reject when the store is down
// (use for authentication); others allow and log.
type Rule struct {
	Class      string
	Limit      core.Limit
	FailClosed bool
}

// Limiter applies rules using a shared store.
type Limiter struct {
	store Store
	now   clock.Now
	log   *slog.Logger
}

// NewLimiter returns a Limiter.
func NewLimiter(store Store, now clock.Now, log *slog.Logger) Limiter {
	return Limiter{store: store, now: now, log: log}
}

// Allow counts one attempt by subject under r and decides it. Subjects
// (IPs, emails, user IDs) are hashed before they reach the store.
func (l Limiter) Allow(ctx context.Context, r Rule, subject string) (core.Decision, error) {
	now := l.now()
	sum := sha256.Sum256([]byte(r.Class + "\x00" + subject))
	key := keyPrefix + r.Class + ":" + hex.EncodeToString(sum[:16])
	counts, err := l.store(ctx, key, r.Limit.Window(), now)
	if err != nil {
		if r.FailClosed {
			return core.Decision{}, ErrUnavailable
		}
		l.log.ErrorContext(ctx, "rate limiter unavailable; allowing request", "class", r.Class, "err", err.Error())
		return core.Decision{Allowed: true}, nil
	}
	return core.Decide(r.Limit, counts, now), nil
}

// Middleware limits requests under r by the subject derived from each
// request (for example the client IP).
func (l Limiter) Middleware(r Rule, subject func(*http.Request) string) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			d, err := l.Allow(req.Context(), r, subject(req))
			if err != nil || !d.Allowed {
				Reject(w, req, d, err)
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

// Reject writes the response for a denied decision: 503 when the limiter
// is unavailable, otherwise 429 with Retry-After.
func Reject(w http.ResponseWriter, r *http.Request, d core.Decision, err error) {
	if err != nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "rate_limiter_unavailable", "please retry shortly")
		return
	}
	w.Header().Set("Retry-After", RetryAfterSeconds(d.RetryAfter))
	httpx.WriteError(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests; retry later")
}

// RetryAfterSeconds renders d as whole seconds, rounded up (minimum 1).
func RetryAfterSeconds(d time.Duration) string {
	return strconv.Itoa(int(math.Max(1, math.Ceil(d.Seconds()))))
}

// incrScript atomically counts an attempt in the current window and reads
// the previous window. Window keys expire after two windows.
const incrScript = `
local curr = redis.call('INCR', KEYS[2])
if curr == 1 then redis.call('PEXPIRE', KEYS[2], ARGV[1]) end
local prev = redis.call('GET', KEYS[1])
return {tonumber(prev) or 0, curr}
`

// RedisStore keeps window counters in Redis as <key>:<window-start-unix>.
func RedisStore(c *redis.Client) Store {
	script := redis.NewScript(incrScript)
	return func(ctx context.Context, key string, window time.Duration, now time.Time) (core.Counts, error) {
		start := core.WindowStart(now, window)
		keys := []string{
			key + ":" + strconv.FormatInt(start.Add(-window).Unix(), 10),
			key + ":" + strconv.FormatInt(start.Unix(), 10),
		}
		res, err := script.Run(ctx, c, keys, (2 * window).Milliseconds()).Int64Slice()
		if err != nil {
			return core.Counts{}, err
		}
		if len(res) != 2 {
			return core.Counts{}, errors.New("ratelimit: unexpected script result")
		}
		return core.Counts{Prev: res[0], Curr: res[1] - 1}, nil
	}
}
