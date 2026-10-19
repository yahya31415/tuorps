package app

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/auth"
	authcore "github.com/yahya31415/tuorps/internal/auth/core"
	authhttp "github.com/yahya31415/tuorps/internal/auth/http"
	"github.com/yahya31415/tuorps/internal/email"
	healthhttp "github.com/yahya31415/tuorps/internal/health/http"
	"github.com/yahya31415/tuorps/internal/platform/clientip"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit"
	"github.com/yahya31415/tuorps/internal/workspace"
	wshttp "github.com/yahya31415/tuorps/internal/workspace/http"
)

const mfaChallengeTTL = 5 * time.Minute

// API is the assembled HTTP API.
type API struct {
	Handler http.Handler  // the full stack: middleware, CSRF protection, routes
	Router  *httpx.Router // for route introspection (OpenAPI sync)
}

// BuildAPI wires every route over deps. Connections are used lazily, so
// building does not touch the network.
func BuildAPI(cfg config.Config, deps *Deps, log *slog.Logger) (*API, error) {
	keys, err := cfg.Encryption.Keyring()
	if err != nil {
		return nil, fmt.Errorf("app: keyring: %w", err)
	}
	proxies := make([]string, len(cfg.TrustedProxies))
	for i, p := range cfg.TrustedProxies {
		proxies[i] = p.String()
	}
	resolver, err := clientip.New(proxies)
	if err != nil {
		return nil, err
	}
	inserter, err := jobs.NewInserter(deps.Pool, log)
	if err != nil {
		return nil, err
	}
	now := clock.System()
	policy := authcore.SessionPolicy{Idle: cfg.Auth.SessionIdle, Absolute: cfg.Auth.SessionAbsolute}
	writer := audit.NewWriter(deps.Pool, ids.System(), now)
	sessions := auth.NewSessionStore(deps.Redis, policy, now, rand.Reader)
	outbox := email.NewOutbox(inserter, keys, rand.Reader)
	limiter := ratelimit.NewLimiter(ratelimit.RedisStore(deps.Redis), now, log)

	rt := httpx.NewRouter()
	healthhttp.Register(rt, deps.Checks(), cfg.ReadinessTimeout, log)
	if err := authhttp.Register(rt, authhttp.Deps{
		Pool:        deps.Pool,
		Writer:      writer,
		Hasher:      auth.NewHasher(authcore.DefaultArgon2(), cfg.Auth.Argon2MaxConcurrency, rand.Reader),
		Sessions:    sessions,
		Challenges:  auth.NewChallengeStore(deps.Redis, mfaChallengeTTL, rand.Reader),
		Mailer:      outbox,
		Keys:        keys,
		Limiter:     limiter,
		ClientIP:    resolver,
		Origin:      cfg.AppOrigin,
		SessionLife: cfg.Auth.SessionAbsolute,
		Now:         now,
		NewID:       ids.System(),
		Rand:        rand.Reader,
		Log:         log,
		Bootstrap:   workspace.Bootstrap,
	}); err != nil {
		return nil, err
	}
	if err := wshttp.Register(rt, wshttp.Deps{
		Pool:     deps.Pool,
		Writer:   writer,
		Sessions: sessions,
		Mailer:   outbox,
		Keys:     keys,
		Limiter:  limiter,
		ClientIP: resolver,
		Origin:   cfg.AppOrigin,
		Now:      now,
		NewID:    ids.System(),
		Rand:     rand.Reader,
		Log:      log,
	}); err != nil {
		return nil, err
	}

	csrf := http.NewCrossOriginProtection()
	if err := csrf.AddTrustedOrigin(cfg.AppOrigin.String()); err != nil {
		return nil, fmt.Errorf("app: trusted origin: %w", err)
	}
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusForbidden, "cross_origin_request", "cross-origin requests are not allowed")
	}))
	return &API{Handler: httpx.Chain(csrf.Handler(rt), Middleware(cfg, log)...), Router: rt}, nil
}
