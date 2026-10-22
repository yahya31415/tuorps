// Package wshttp serves the workspace API: workspaces, brands, members,
// per-brand roles, invitations and the activity log. Every request inside
// a workspace runs as a principal loaded under row-level security; a
// workspace or brand the caller cannot see is reported as not found.
package wshttp

import (
	"context"
	"errors"
	"io"
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
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/clientip"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit"
	ratecore "github.com/yahya31415/tuorps/internal/platform/ratelimit/core"
	"github.com/yahya31415/tuorps/internal/workspace"
	"github.com/yahya31415/tuorps/internal/workspace/core"
)

const (
	maxBody       = 16 << 10
	defaultPage   = 50
	maxPage       = 100
	invitationTTL = 7 * 24 * time.Hour
)

// Mailer queues an email in an audited write transaction.
type Mailer interface {
	Enqueue(ctx context.Context, tx audit.Tx, m emailcore.Message) error
}

// Deps are the handlers' collaborators.
type Deps struct {
	Pool     *pgxpool.Pool
	Writer   audit.Writer
	Sessions auth.SessionStore
	Mailer   Mailer
	Keys     *keyring.Keyring
	Limiter  ratelimit.Limiter
	ClientIP clientip.Resolver
	Origin   *url.URL
	Now      clock.Now
	NewID    ids.NewID
	Rand     io.Reader
	Log      *slog.Logger
}

type handlers struct {
	Deps
	ipKey      []byte
	writeLimit ratelimit.Rule
}

// Register mounts the workspace routes on rt.
func Register(rt *httpx.Router, d Deps) error {
	l, err := ratecore.NewLimit(120, time.Minute)
	if err != nil {
		return err
	}
	h := &handlers{Deps: d, ipKey: d.Keys.DeriveKey("audit-ip"), writeLimit: ratelimit.Rule{Class: "write_user", Limit: l}}
	session := authhttp.RequireSession(d.Sessions)
	user := func(f http.HandlerFunc) http.Handler { return session(h.limitWrites(f)) }
	inWS := func(f http.HandlerFunc) http.Handler { return session(h.limitWrites(h.withWorkspace(f))) }

	rt.Handle("GET /v1/workspaces", user(h.listMine))
	rt.Handle("POST /v1/workspaces", user(h.create))
	rt.Handle("POST /v1/invitations/accept", user(h.accept))
	rt.Handle("GET /v1/workspaces/{ws}", inWS(h.get))
	rt.Handle("PATCH /v1/workspaces/{ws}", inWS(h.rename))
	rt.Handle("GET /v1/workspaces/{ws}/brands", inWS(h.listBrands))
	rt.Handle("POST /v1/workspaces/{ws}/brands", inWS(h.createBrand))
	rt.Handle("GET /v1/workspaces/{ws}/brands/{brand}", inWS(h.getBrand))
	rt.Handle("PATCH /v1/workspaces/{ws}/brands/{brand}", inWS(h.renameBrand))
	rt.Handle("DELETE /v1/workspaces/{ws}/brands/{brand}", inWS(h.archiveBrand))
	rt.Handle("PUT /v1/workspaces/{ws}/brands/{brand}/members/{user}", inWS(h.setRole))
	rt.Handle("DELETE /v1/workspaces/{ws}/brands/{brand}/members/{user}", inWS(h.removeRole))
	rt.Handle("GET /v1/workspaces/{ws}/members", inWS(h.listMembers))
	rt.Handle("DELETE /v1/workspaces/{ws}/members/{user}", inWS(h.removeMember))
	rt.Handle("GET /v1/workspaces/{ws}/invitations", inWS(h.listInvitations))
	rt.Handle("POST /v1/workspaces/{ws}/invitations", inWS(h.createInvitation))
	rt.Handle("DELETE /v1/workspaces/{ws}/invitations/{id}", inWS(h.revokeInvitation))
	rt.Handle("GET /v1/workspaces/{ws}/audit-log", inWS(h.auditLog))
	return nil
}

// limitWrites applies the per-user write limit to mutating requests
// (fail-open: a Redis outage must not stop customers working).
func (h *handlers) limitWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			id, _ := authhttp.IdentityFrom(r.Context())
			if d, err := h.Limiter.Allow(r.Context(), h.writeLimit, id.UserID.String()); err != nil || !d.Allowed {
				ratelimit.Reject(w, r, d, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type principalKey struct{}

func principalFrom(ctx context.Context) core.Principal {
	p, _ := ctx.Value(principalKey{}).(core.Principal)
	return p
}

// withWorkspace loads the caller's principal for {ws}; non-members get 404.
func (h *handlers) withWorkspace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := authhttp.IdentityFrom(r.Context())
		ws, err := uuid.Parse(r.PathValue("ws"))
		if err != nil {
			notFound(w, r)
			return
		}
		var p core.Principal
		err = postgres.InReadTx(r.Context(), h.Pool, postgres.Scope{WorkspaceID: ws, UserID: id.UserID}, func(tx pgx.Tx) error {
			var err error
			p, err = workspace.LoadPrincipal(r.Context(), tx, postgres.Scope{WorkspaceID: ws, UserID: id.UserID})
			return err
		})
		switch {
		case errors.Is(err, workspace.ErrNotFound):
			notFound(w, r)
		case err != nil:
			h.internalError(w, r, "principal", err)
		default:
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
		}
	})
}

func scopeOf(p core.Principal) postgres.Scope {
	return postgres.Scope{WorkspaceID: p.WorkspaceID(), UserID: p.UserID()}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusNotFound, "not_found", "not found")
}

func forbidden(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "you do not have permission to do this")
}

func (h *handlers) internalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.Log.ErrorContext(r.Context(), "workspace handler failed", "op", op, "err", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
	httpx.WriteError(w, r, http.StatusInternalServerError, "internal", "internal error")
}

func (h *handlers) meta(r *http.Request) auditcore.Meta {
	return audit.MetaFromRequest(r, h.ClientIP, h.ipKey)
}

// event builds an audit event from constant names and IDs.
func event(action, targetType string, target uuid.UUID, details map[string]string) auditcore.Event {
	ev, _ := auditcore.NewEvent(action, targetType, target.String(), details)
	return ev
}

// write runs fn in an audited transaction scoped to p and maps repository
// errors to responses; it reports success.
func (h *handlers) write(w http.ResponseWriter, r *http.Request, p core.Principal, ev auditcore.Event, fn func(audit.Tx) error) bool {
	err := h.Writer.InWriteTx(r.Context(), scopeOf(p), ev, h.meta(r), fn)
	switch {
	case err == nil:
		return true
	case errors.Is(err, workspace.ErrNotFound):
		notFound(w, r)
	case errors.Is(err, workspace.ErrNameTaken):
		httpx.WriteError(w, r, http.StatusConflict, "name_taken", workspace.ErrNameTaken.Error())
	case errors.Is(err, workspace.ErrOwner):
		httpx.WriteError(w, r, http.StatusConflict, "cannot_remove_owner", workspace.ErrOwner.Error())
	default:
		h.internalError(w, r, ev.Action(), err)
	}
	return false
}

// read runs fn in a read-only transaction scoped to p.
func (h *handlers) read(w http.ResponseWriter, r *http.Request, s postgres.Scope, fn func(pgx.Tx) error) bool {
	err := postgres.InReadTx(r.Context(), h.Pool, s, fn)
	switch {
	case err == nil:
		return true
	case errors.Is(err, workspace.ErrNotFound):
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

func page(w http.ResponseWriter, r *http.Request) (httpx.Page, bool) {
	p, err := httpx.ParsePage(r, defaultPage, maxPage)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_page", err.Error())
		return httpx.Page{}, false
	}
	return p, true
}
