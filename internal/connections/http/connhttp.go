// Package connhttp serves the connection center: starting an OAuth flow,
// the provider callback, choosing accounts, and listing, reconnecting and
// disconnecting connections (ADRs 0008 and 0009). Brands the caller cannot
// see are reported as not found; managing connections needs the brand's
// connection.manage permission.
package connhttp

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/auth"
	authhttp "github.com/yahya31415/tuorps/internal/auth/http"
	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/clientip"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit"
	ratecore "github.com/yahya31415/tuorps/internal/platform/ratelimit/core"
	"github.com/yahya31415/tuorps/internal/workspace"
	wscore "github.com/yahya31415/tuorps/internal/workspace/core"
	wshttp "github.com/yahya31415/tuorps/internal/workspace/http"
)

const (
	maxBody     = 16 << 10
	defaultPage = 50
	maxPage     = 100
	maxSelect   = 50 // accounts per connect request
	expiringIn  = 14 * 24 * time.Hour
)

// Platform is one configured provider: its public OAuth client and its
// operations.
type Platform struct {
	Client core.OAuthClient
	Ports  connections.Provider
}

// Deps are the handlers' collaborators.
type Deps struct {
	Pool      *pgxpool.Pool
	Writer    audit.Writer
	Sessions  auth.SessionStore
	States    connections.StateStore
	Vault     connections.Vault
	Platforms map[core.Provider]Platform // configured providers only
	Keys      *keyring.Keyring
	Limiter   ratelimit.Limiter
	ClientIP  clientip.Resolver
	Origin    *url.URL // the browser app: callback redirects go here
	Now       clock.Now
	NewID     ids.NewID
	Log       *slog.Logger
}

type handlers struct {
	Deps
	ipKey     []byte
	oauthRule ratelimit.Rule
}

// Register mounts the connection routes on rt. Every route is registered
// whether or not a provider is configured; unconfigured ones answer
// provider_not_configured.
func Register(rt *httpx.Router, d Deps) error {
	writes, err := ratecore.NewLimit(120, time.Minute)
	if err != nil {
		return err
	}
	flows, err := ratecore.NewLimit(30, time.Minute)
	if err != nil {
		return err
	}
	h := &handlers{Deps: d, ipKey: d.Keys.DeriveKey("audit-ip"), oauthRule: ratelimit.Rule{Class: "oauth_user", Limit: flows, FailClosed: true}}
	session := authhttp.RequireSession(d.Sessions)
	limitWrites := wshttp.LimitWrites(d.Limiter, ratelimit.Rule{Class: "write_user", Limit: writes})
	withWorkspace := wshttp.WithWorkspace(d.Pool, d.Log)
	inWS := func(f http.HandlerFunc) http.Handler { return session(limitWrites(withWorkspace(f))) }
	startsFlow := func(f http.HandlerFunc) http.Handler { return session(limitWrites(withWorkspace(h.limitFlows(f)))) }

	const brand = "/v1/workspaces/{ws}/brands/{brand}"
	rt.Handle("POST "+brand+"/connections/authorize", startsFlow(h.authorize))
	rt.Handle("POST "+brand+"/connections/{id}/reconnect", startsFlow(h.reconnect))
	rt.Handle("GET "+brand+"/connections", inWS(h.list))
	rt.Handle("POST "+brand+"/connections", inWS(h.create))
	rt.Handle("GET "+brand+"/connections/{id}", inWS(h.get))
	rt.Handle("DELETE "+brand+"/connections/{id}", inWS(h.disconnect))
	rt.Handle("GET "+brand+"/grants/{grant}/accounts", inWS(h.accounts))
	rt.Handle("GET /v1/oauth/{provider}/callback", http.HandlerFunc(h.callback))
	return nil
}

// limitFlows limits how many OAuth flows a user may start (fail-closed:
// each one stores state).
func (h *handlers) limitFlows(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := authhttp.IdentityFrom(r.Context())
		if d, err := h.Limiter.Allow(r.Context(), h.oauthRule, id.UserID.String()); err != nil || !d.Allowed {
			ratelimit.Reject(w, r, d, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func scopeOf(p wscore.Principal) postgres.Scope {
	return postgres.Scope{WorkspaceID: p.WorkspaceID(), UserID: p.UserID()}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusNotFound, "not_found", "not found")
}

func forbidden(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "you do not have permission to do this")
}

func (h *handlers) internalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.Log.ErrorContext(r.Context(), "connection handler failed", "op", op, "err", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
	httpx.WriteError(w, r, http.StatusInternalServerError, "internal", "internal error")
}

// providerError answers a failed platform call without its details.
func (h *handlers) providerError(w http.ResponseWriter, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, core.ErrReauthRequired):
		httpx.WriteError(w, r, http.StatusConflict, "grant_expired", "the platform no longer accepts this login; connect again")
	case errors.Is(err, core.ErrRateLimited), errors.Is(err, core.ErrProviderUnavailable):
		httpx.WriteError(w, r, http.StatusBadGateway, "provider_unavailable", "the platform is not responding; retry shortly")
	default:
		h.Log.WarnContext(r.Context(), "platform call refused", "op", op, "err", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
		httpx.WriteError(w, r, http.StatusBadGateway, "provider_error", "the platform refused the request")
	}
}

func (h *handlers) meta(r *http.Request) auditcore.Meta {
	return audit.MetaFromRequest(r, h.ClientIP, h.ipKey)
}

// event builds an audit event from constant names and IDs.
func event(action, targetType string, target uuid.UUID, details map[string]string) auditcore.Event {
	ev, _ := auditcore.NewEvent(action, targetType, target.String(), details)
	return ev
}

// write runs fn in an audited transaction scoped to s and maps repository
// errors to responses; it reports success.
func (h *handlers) write(w http.ResponseWriter, r *http.Request, s postgres.Scope, ev auditcore.Event, fn func(audit.Tx) error) bool {
	err := h.Writer.InWriteTx(r.Context(), s, ev, h.meta(r), fn)
	switch {
	case err == nil:
		return true
	case errors.Is(err, connections.ErrNotFound):
		notFound(w, r)
	case errors.Is(err, connections.ErrAlreadyConnected):
		httpx.WriteError(w, r, http.StatusConflict, "already_connected", connections.ErrAlreadyConnected.Error())
	default:
		h.internalError(w, r, ev.Action(), err)
	}
	return false
}

// read runs fn in a read-only transaction scoped to p.
func (h *handlers) read(w http.ResponseWriter, r *http.Request, p wscore.Principal, fn func(pgx.Tx) error) bool {
	err := postgres.InReadTx(r.Context(), h.Pool, scopeOf(p), fn)
	switch {
	case err == nil:
		return true
	case errors.Is(err, connections.ErrNotFound), errors.Is(err, workspace.ErrNotFound):
		notFound(w, r)
	default:
		h.internalError(w, r, "read", err)
	}
	return false
}

func pathID(r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	return id, err == nil
}

// brandFor resolves {brand} for p: unknown, foreign, unreadable and
// archived brands are 404; manage additionally needs connection.manage
// (403).
func (h *handlers) brandFor(w http.ResponseWriter, r *http.Request, p wscore.Principal, manage bool) (uuid.UUID, bool) {
	id, ok := pathID(r, "brand")
	if !ok || !wscore.Can(p, wscore.PermConnectionRead, id) {
		notFound(w, r)
		return uuid.Nil(), false
	}
	var b workspace.Brand
	if !h.read(w, r, p, func(tx pgx.Tx) error {
		var err error
		b, err = workspace.GetBrand(r.Context(), tx, p.WorkspaceID(), id)
		return err
	}) {
		return uuid.Nil(), false
	}
	if b.Archived {
		notFound(w, r)
		return uuid.Nil(), false
	}
	if manage && !wscore.Can(p, wscore.PermConnectionManage, id) {
		forbidden(w, r)
		return uuid.Nil(), false
	}
	return id, true
}

// connectionJSON is a connection as the API shows it.
type connectionJSON struct {
	ID            uuid.UUID  `json:"id"`
	BrandID       uuid.UUID  `json:"brand_id"`
	Provider      string     `json:"provider"`
	Kind          string     `json:"kind"`
	ExternalID    string     `json:"external_id"`
	DisplayName   string     `json:"display_name"`
	Status        string     `json:"status"`
	Expiring      bool       `json:"expiring"`
	ExpiresAt     *time.Time `json:"expires_at"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastError     *string    `json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
}

func toConnection(c connections.Connection, now time.Time) connectionJSON {
	out := connectionJSON{
		ID: c.ID, BrandID: c.BrandID, Provider: string(c.Provider), Kind: string(c.Kind), ExternalID: c.ExternalID,
		DisplayName: c.DisplayName, Status: string(c.Status), CreatedAt: c.CreatedAt,
		ExpiresAt: optTime(c.TokenExpiresAt), LastCheckedAt: optTime(c.LastCheckedAt),
	}
	if c.LastError != "" {
		e := string(c.LastError)
		out.LastError = &e
	}
	out.Expiring = c.Status != core.StatusDisconnected && !c.TokenExpiresAt.IsZero() && c.TokenExpiresAt.Sub(now) <= expiringIn
	return out
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
