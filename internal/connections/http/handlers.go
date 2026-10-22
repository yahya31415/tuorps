package connhttp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/yahya31415/tuorps/internal/audit"
	authhttp "github.com/yahya31415/tuorps/internal/auth/http"
	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/workspace"
	wscore "github.com/yahya31415/tuorps/internal/workspace/core"
	wshttp "github.com/yahya31415/tuorps/internal/workspace/http"
)

var (
	errProvider = errors.New("provider must be one of meta, instagram, linkedin, google")
	errSelect   = errors.New("grant_id must be an id and external_ids 1-50 distinct account ids")
)

type authorizeRequest struct {
	Provider string `json:"provider"`
}

func (a authorizeRequest) Validate() error {
	if _, err := core.ParseProvider(a.Provider); err != nil {
		return errProvider
	}
	return nil
}

type connectRequest struct {
	GrantID     string   `json:"grant_id"`
	ExternalIDs []string `json:"external_ids"`
}

func (c connectRequest) Validate() error {
	if _, err := uuid.Parse(c.GrantID); err != nil || len(c.ExternalIDs) == 0 || len(c.ExternalIDs) > maxSelect {
		return errSelect
	}
	seen := map[string]bool{}
	for _, id := range c.ExternalIDs {
		if id == "" || len(id) > 128 || seen[id] {
			return errSelect
		}
		seen[id] = true
	}
	return nil
}

type authorizeResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

// platform returns p's configuration, answering 422 when it has none.
func (h *handlers) platform(w http.ResponseWriter, r *http.Request, p core.Provider) (Platform, bool) {
	pl, ok := h.Platforms[p]
	if !ok {
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, "provider_not_configured", "this platform is not available yet")
	}
	return pl, ok
}

// startFlow stores the state and answers with the consent URL.
func (h *handlers) startFlow(w http.ResponseWriter, r *http.Request, p wscore.Principal, brand uuid.UUID, provider core.Provider, reconnect uuid.UUID) {
	pl, ok := h.platform(w, r, provider)
	if !ok {
		return
	}
	id, _ := authhttp.IdentityFrom(r.Context())
	state, err := h.States.Create(r.Context(), connections.StatePayload{
		UserID: id.UserID, SessionHash: id.SessionHash(), WorkspaceID: p.WorkspaceID(), BrandID: brand, Provider: provider, ReconnectID: reconnect,
	})
	if err != nil {
		h.internalError(w, r, "state", err)
		return
	}
	u, err := core.AuthorizeURL(provider, pl.Client, state)
	if err != nil {
		h.internalError(w, r, "authorize_url", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, authorizeResponse{AuthorizeURL: u})
}

func (h *handlers) authorize(w http.ResponseWriter, r *http.Request) {
	p, _ := wshttp.PrincipalFrom(r.Context())
	brand, ok := h.brandFor(w, r, p, true)
	if !ok {
		return
	}
	req, ok := httpx.Decode[authorizeRequest](w, r, maxBody)
	if !ok {
		return
	}
	h.startFlow(w, r, p, brand, core.Provider(req.Provider), uuid.Nil())
}

func (h *handlers) reconnect(w http.ResponseWriter, r *http.Request) {
	p, _ := wshttp.PrincipalFrom(r.Context())
	brand, ok := h.brandFor(w, r, p, true)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w, r)
		return
	}
	var c connections.Connection
	if !h.read(w, r, p, func(tx pgx.Tx) error {
		var err error
		c, err = connections.GetConnection(r.Context(), tx, p.WorkspaceID(), brand, id)
		return err
	}) {
		return
	}
	if c.Status == core.StatusDisconnected {
		notFound(w, r)
		return
	}
	h.startFlow(w, r, p, brand, c.Provider, c.ID)
}

// Callback failure reasons: a closed set, shown by the app's error page.
const (
	reasonSignedOut      = "signed_out"
	reasonInvalidState   = "invalid_state"
	reasonDenied         = "denied"
	reasonProviderError  = "provider_error"
	reasonNotConfigured  = "not_configured"
	reasonForbidden      = "forbidden"
	reasonExchangeFailed = "exchange_failed"
	reasonAccountMissing = "account_missing"
	reasonUnavailable    = "unavailable"
)

// redirect sends the browser to the app; all data goes in the fragment so
// it never reaches server logs or Referer headers.
func (h *handlers) redirect(w http.ResponseWriter, r *http.Request, path string, frag url.Values) {
	u := *h.Origin
	u.Path, u.RawQuery, u.Fragment = path, "", frag.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, reason string) {
	h.redirect(w, r, "/connections/error", url.Values{"reason": {reason}})
}

// callback completes an OAuth flow (ADR 0008). The state is consumed first,
// so it is single-use even when the flow fails; it must belong to this
// provider, user and session; the permission is checked again because
// roles can change while the user is at the provider.
func (h *handlers) callback(w http.ResponseWriter, r *http.Request) {
	ctx, q := r.Context(), r.URL.Query()
	id, ok, err := authhttp.LookupSession(r, h.Sessions)
	switch {
	case err != nil:
		h.fail(w, r, reasonUnavailable)
		return
	case !ok:
		h.fail(w, r, reasonSignedOut)
		return
	}
	st, err := h.States.Consume(ctx, q.Get("state"))
	if err != nil {
		reason := reasonInvalidState
		if !errors.Is(err, connections.ErrStateNotFound) {
			reason = reasonUnavailable
		}
		h.fail(w, r, reason)
		return
	}
	if string(st.Provider) != r.PathValue("provider") || st.UserID != id.UserID || st.SessionHash != id.SessionHash() {
		h.fail(w, r, reasonInvalidState)
		return
	}
	if e := q.Get("error"); e != "" {
		reason := reasonProviderError
		if slices.Contains([]string{"access_denied", "user_cancelled_login", "user_cancelled_authorize"}, e) {
			reason = reasonDenied
		}
		h.fail(w, r, reason)
		return
	}
	code := q.Get("code")
	pl, configured := h.Platforms[st.Provider]
	switch {
	case code == "":
		h.fail(w, r, reasonProviderError)
		return
	case !configured:
		h.fail(w, r, reasonNotConfigured)
		return
	case !h.mayManage(ctx, st):
		h.fail(w, r, reasonForbidden)
		return
	}
	ts, err := pl.Ports.Exchange(ctx, code)
	if err != nil {
		h.Log.WarnContext(ctx, "oauth exchange failed", "provider", string(st.Provider), "err", err.Error(), "request_id", httpx.RequestIDFrom(ctx))
		h.fail(w, r, reasonExchangeFailed)
		return
	}
	grant, err := h.newGrant(ctx, st, pl, ts)
	if err != nil {
		h.Log.ErrorContext(ctx, "sealing grant failed", "err", err.Error())
		h.fail(w, r, reasonUnavailable)
		return
	}
	scope := postgres.Scope{WorkspaceID: st.WorkspaceID, UserID: st.UserID}
	if st.ReconnectID != uuid.Nil() {
		h.finishReconnect(w, r, scope, st, pl, ts, grant)
		return
	}
	details := map[string]string{"provider": string(st.Provider), "brand_id": st.BrandID.String()}
	if err := h.Writer.InWriteTx(ctx, scope, event("connection.grant", "oauth_grant", grant.ID, details), h.meta(r), func(tx audit.Tx) error {
		return connections.InsertGrant(ctx, tx, grant)
	}); err != nil {
		h.Log.ErrorContext(ctx, "storing grant failed", "err", err.Error())
		h.fail(w, r, reasonUnavailable)
		return
	}
	h.redirect(w, r, "/connections/select", url.Values{"grant": {grant.ID.String()}, "brand": {st.BrandID.String()}, "workspace": {st.WorkspaceID.String()}})
}

// mayManage re-checks, at callback time, that the user may still manage the
// state's brand and that the brand is live.
func (h *handlers) mayManage(ctx context.Context, st connections.StatePayload) bool {
	scope := postgres.Scope{WorkspaceID: st.WorkspaceID, UserID: st.UserID}
	ok := false
	err := postgres.InReadTx(ctx, h.Pool, scope, func(tx pgx.Tx) error {
		p, err := workspace.LoadPrincipal(ctx, tx, scope)
		if err != nil {
			return err
		}
		b, err := workspace.GetBrand(ctx, tx, st.WorkspaceID, st.BrandID)
		if err != nil {
			return err
		}
		ok = !b.Archived && wscore.Can(p, wscore.PermConnectionManage, st.BrandID)
		return nil
	})
	return err == nil && ok
}

// newGrant seals the exchanged tokens into a grant row.
func (h *handlers) newGrant(ctx context.Context, st connections.StatePayload, pl Platform, ts core.TokenSet) (connections.NewGrant, error) {
	now := h.Now()
	g := connections.NewGrant{
		ID: h.NewID(), BrandID: st.BrandID, Provider: st.Provider, ProviderUserID: ts.ProviderUserID(),
		Scopes: ts.Scopes(), MissingScopes: missingScopes(st.Provider, ts.Scopes()),
		TokenIssuedAt: now, CreatedBy: st.UserID, Now: now,
	}
	if ts.ExpiresIn() > 0 {
		g.AccessExpiresAt = now.Add(ts.ExpiresIn())
	}
	var err error
	if g.AccessTokenEnc, err = h.Vault.Seal(g.ID, core.FieldAccessToken, ts.AccessToken()); err != nil {
		return connections.NewGrant{}, err
	}
	if ts.RefreshToken() != "" {
		if g.RefreshTokenEnc, err = h.Vault.Seal(g.ID, core.FieldRefreshToken, ts.RefreshToken()); err != nil {
			return connections.NewGrant{}, err
		}
	}
	// Meta: learn when data access lapses (best effort; the daily check retries).
	if st.Provider == core.ProviderMeta && pl.Ports.Check != nil {
		if health, err := pl.Ports.Check(ctx, ts.AccessToken()); err == nil {
			g.DataAccessExpiresAt = health.DataAccessExpiresAt
		}
	}
	return g, nil
}

// missingScopes lists requested scopes the user did not grant. A response
// that names no scopes says nothing either way.
func missingScopes(p core.Provider, granted []string) []string {
	if len(granted) == 0 {
		return nil
	}
	var missing []string
	for _, s := range core.RequestedScopes(p) {
		if !slices.Contains(granted, s) {
			missing = append(missing, s)
		}
	}
	return missing
}

// finishReconnect moves the connection being reconnected to the new grant,
// if the new login still offers that account, and wipes the old grant's
// tokens once nothing uses it.
func (h *handlers) finishReconnect(w http.ResponseWriter, r *http.Request, scope postgres.Scope, st connections.StatePayload, pl Platform, ts core.TokenSet, grant connections.NewGrant) {
	ctx := r.Context()
	var c connections.Connection
	if err := postgres.InReadTx(ctx, h.Pool, scope, func(tx pgx.Tx) error {
		var err error
		c, err = connections.GetConnection(ctx, tx, st.WorkspaceID, st.BrandID, st.ReconnectID)
		return err
	}); err != nil {
		h.fail(w, r, reasonAccountMissing)
		return
	}
	accounts, err := pl.Ports.ListAccounts(ctx, ts.AccessToken())
	if err != nil {
		h.fail(w, r, reasonExchangeFailed)
		return
	}
	i := slices.IndexFunc(accounts, func(a core.Account) bool { return a.Kind == c.Kind && a.ExternalID == c.ExternalID })
	if i < 0 {
		h.fail(w, r, reasonAccountMissing)
		return
	}
	repoint, err := h.connectionToken(c.ID, accounts[i], grant)
	if err != nil {
		h.fail(w, r, reasonUnavailable)
		return
	}
	details := map[string]string{"provider": string(st.Provider), "grant_id": grant.ID.String()}
	err = h.Writer.InWriteTx(ctx, scope, event("connection.reconnect", "connection", c.ID, details), h.meta(r), func(tx audit.Tx) error {
		if err := connections.InsertGrant(ctx, tx, grant); err != nil {
			return err
		}
		if err := connections.RepointConnection(ctx, tx, st.BrandID, c.ID, connections.Repoint{
			GrantID: grant.ID, TokenEnc: repoint.TokenEnc, TokenExpiresAt: repoint.TokenExpiresAt, NextDueAt: repoint.NextDueAt, Now: grant.Now,
		}); err != nil {
			return err
		}
		return wipeIfUnused(ctx, tx, st.BrandID, c.GrantID, grant.Now)
	})
	if err != nil {
		h.Log.ErrorContext(ctx, "reconnect failed", "err", err.Error())
		h.fail(w, r, reasonUnavailable)
		return
	}
	h.redirect(w, r, "/connections/done", url.Values{"connection": {c.ID.String()}, "brand": {st.BrandID.String()}, "workspace": {st.WorkspaceID.String()}})
}

// wipeIfUnused wipes a grant's tokens when no live connection uses it.
func wipeIfUnused(ctx context.Context, tx audit.Tx, brand, grant uuid.UUID, now time.Time) error {
	n, err := connections.LiveConnectionsOnGrant(ctx, tx.Pgx(), tx.Scope().WorkspaceID, brand, grant)
	if err != nil || n > 0 {
		return err
	}
	return connections.WipeGrantTokens(ctx, tx, brand, grant, now)
}

// connectionToken seals an account's own token for connection id and
// computes its expiry and first maintenance time.
func (h *handlers) connectionToken(id uuid.UUID, a core.Account, g connections.NewGrant) (connections.NewConnection, error) {
	c := connections.NewConnection{ID: id, Kind: a.Kind, ExternalID: a.ExternalID, DisplayName: displayName(a), Now: g.Now}
	if a.Token.Present() {
		enc, err := h.Vault.Seal(id, core.FieldPageToken, a.Token.AccessToken())
		if err != nil {
			return connections.NewConnection{}, err
		}
		c.TokenEnc = enc
	} else {
		// Connected with the grant's own token, which may expire.
		c.TokenExpiresAt = g.AccessExpiresAt
	}
	state := core.ConnState{
		Provider: g.Provider, Status: core.StatusActive, AccessExpiresAt: c.TokenExpiresAt, TokenIssuedAt: g.TokenIssuedAt,
		LastCheckedAt: g.Now, HasRefreshToken: g.RefreshTokenEnc != "", KeyID: h.Keys.ActiveKeyID(), ActiveKeyID: h.Keys.ActiveKeyID(),
	}
	if a.Kind == core.KindInstagramAccount {
		// Page permissions never lose data access; instagram_* ones can.
		state.DataAccessExpiresAt = g.DataAccessExpiresAt
	}
	c.NextDueAt = core.Evaluate(state, g.Now).NextDue
	return c, nil
}

func displayName(a core.Account) string {
	if a.DisplayName == "" {
		return a.ExternalID
	}
	if len(a.DisplayName) > 200 {
		return a.DisplayName[:200]
	}
	return a.DisplayName
}

// grantFor loads the caller's own grant on brand with its access token.
// Another user's grant, a wiped grant and a missing one are all 404.
func (h *handlers) grantFor(w http.ResponseWriter, r *http.Request, p wscore.Principal, brand, id uuid.UUID) (connections.Grant, string, bool) {
	var g connections.Grant
	if !h.read(w, r, p, func(tx pgx.Tx) error {
		var err error
		g, err = connections.GetGrant(r.Context(), tx, p.WorkspaceID(), brand, id)
		return err
	}) {
		return connections.Grant{}, "", false
	}
	if g.CreatedBy != p.UserID() || g.AccessTokenEnc == "" {
		notFound(w, r)
		return connections.Grant{}, "", false
	}
	tok, err := h.Vault.Open(g.ID, core.FieldAccessToken, g.AccessTokenEnc)
	if err != nil {
		h.internalError(w, r, "open_grant", err)
		return connections.Grant{}, "", false
	}
	return g, tok, true
}

type accountJSON struct {
	Kind        string `json:"kind"`
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Connected   bool   `json:"connected"`
}

// accounts lists what the caller's grant can connect, marking accounts
// already connected to this brand.
func (h *handlers) accounts(w http.ResponseWriter, r *http.Request) {
	p, _ := wshttp.PrincipalFrom(r.Context())
	brand, ok := h.brandFor(w, r, p, false)
	if !ok {
		return
	}
	grantID, ok := pathID(r, "grant")
	if !ok {
		notFound(w, r)
		return
	}
	g, tok, ok := h.grantFor(w, r, p, brand, grantID)
	if !ok {
		return
	}
	pl, ok := h.platform(w, r, g.Provider)
	if !ok {
		return
	}
	accts, err := pl.Ports.ListAccounts(r.Context(), tok)
	if err != nil {
		h.providerError(w, r, "accounts", err)
		return
	}
	live, ok := h.liveAccounts(w, r, p, brand)
	if !ok {
		return
	}
	items := make([]accountJSON, 0, len(accts))
	for _, a := range accts {
		items = append(items, accountJSON{Kind: string(a.Kind), ExternalID: a.ExternalID, DisplayName: displayName(a), Connected: live[string(a.Kind)+"/"+a.ExternalID]})
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.ListResponse[accountJSON]{Items: items})
}

// liveAccounts is the set of "kind/external_id" live on brand.
func (h *handlers) liveAccounts(w http.ResponseWriter, r *http.Request, p wscore.Principal, brand uuid.UUID) (map[string]bool, bool) {
	live := map[string]bool{}
	ok := h.read(w, r, p, func(tx pgx.Tx) error {
		after := uuid.Nil()
		for {
			page, err := connections.ListConnections(r.Context(), tx, p.WorkspaceID(), brand, after, maxPage)
			if err != nil {
				return err
			}
			for _, c := range page {
				if c.Status != core.StatusDisconnected {
					live[string(c.Kind)+"/"+c.ExternalID] = true
				}
			}
			if len(page) < maxPage {
				return nil
			}
			after = page[len(page)-1].ID
		}
	})
	return live, ok
}

// create connects the chosen accounts of the caller's grant. The accounts
// are fetched again from the platform: only ones it offers are accepted.
func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	p, _ := wshttp.PrincipalFrom(r.Context())
	brand, ok := h.brandFor(w, r, p, true)
	if !ok {
		return
	}
	req, ok := httpx.Decode[connectRequest](w, r, maxBody)
	if !ok {
		return
	}
	g, tok, ok := h.grantFor(w, r, p, brand, uuid.MustParse(req.GrantID))
	if !ok {
		return
	}
	pl, ok := h.platform(w, r, g.Provider)
	if !ok {
		return
	}
	offered, err := pl.Ports.ListAccounts(r.Context(), tok)
	if err != nil {
		h.providerError(w, r, "accounts", err)
		return
	}
	ng := connections.NewGrant{ID: g.ID, Provider: g.Provider, AccessExpiresAt: g.AccessExpiresAt, DataAccessExpiresAt: g.DataAccessExpiresAt,
		TokenIssuedAt: g.TokenIssuedAt, RefreshTokenEnc: g.RefreshTokenEnc, Now: h.Now()}
	var news []connections.NewConnection
	for _, ext := range req.ExternalIDs {
		i := slices.IndexFunc(offered, func(a core.Account) bool { return a.ExternalID == ext })
		if i < 0 {
			httpx.WriteError(w, r, http.StatusUnprocessableEntity, "unknown_account", "this login does not offer one of the chosen accounts")
			return
		}
		c, err := h.connectionToken(h.NewID(), offered[i], ng)
		if err != nil {
			h.internalError(w, r, "seal", err)
			return
		}
		c.BrandID, c.GrantID = brand, g.ID
		news = append(news, c)
	}
	details := map[string]string{"provider": string(g.Provider), "count": strconv.Itoa(len(news)), "grant_id": g.ID.String()}
	var created []connections.Connection
	if !h.write(w, r, scopeOf(p), event("connection.create", "brand", brand, details), func(tx audit.Tx) error {
		for _, c := range news {
			if err := connections.InsertConnection(r.Context(), tx, c); err != nil {
				return err
			}
			got, err := connections.GetConnection(r.Context(), tx.Pgx(), p.WorkspaceID(), brand, c.ID)
			if err != nil {
				return err
			}
			created = append(created, got)
		}
		return nil
	}) {
		return
	}
	items := make([]connectionJSON, 0, len(created))
	for _, c := range created {
		items = append(items, toConnection(c, ng.Now))
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, httpx.ListResponse[connectionJSON]{Items: items})
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	p, _ := wshttp.PrincipalFrom(r.Context())
	brand, ok := h.brandFor(w, r, p, false)
	if !ok {
		return
	}
	pg, err := httpx.ParsePage(r, defaultPage, maxPage)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_page", err.Error())
		return
	}
	var cs []connections.Connection
	if !h.read(w, r, p, func(tx pgx.Tx) error {
		var err error
		cs, err = connections.ListConnections(r.Context(), tx, p.WorkspaceID(), brand, pg.After, pg.Limit)
		return err
	}) {
		return
	}
	now := h.Now()
	items := make([]connectionJSON, 0, len(cs))
	for _, c := range cs {
		items = append(items, toConnection(c, now))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, httpx.NewList(items, pg.Limit, func(c connectionJSON) uuid.UUID { return c.ID }))
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	p, _ := wshttp.PrincipalFrom(r.Context())
	brand, ok := h.brandFor(w, r, p, false)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w, r)
		return
	}
	var c connections.Connection
	if h.read(w, r, p, func(tx pgx.Tx) error {
		var err error
		c, err = connections.GetConnection(r.Context(), tx, p.WorkspaceID(), brand, id)
		return err
	}) {
		_ = httpx.WriteJSON(w, http.StatusOK, toConnection(c, h.Now()))
	}
}

// disconnect wipes the connection's token, and its grant's tokens once no
// live connection uses them. It does not revoke at the platform: Meta and
// Google revoke every grant of that platform user, which would cut other
// brands' and workspaces' connections.
func (h *handlers) disconnect(w http.ResponseWriter, r *http.Request) {
	p, _ := wshttp.PrincipalFrom(r.Context())
	brand, ok := h.brandFor(w, r, p, true)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w, r)
		return
	}
	now := h.Now()
	if h.write(w, r, scopeOf(p), event("connection.disconnect", "connection", id, nil), func(tx audit.Tx) error {
		c, err := connections.GetConnection(r.Context(), tx.Pgx(), p.WorkspaceID(), brand, id)
		if err != nil {
			return err
		}
		if err := connections.Disconnect(r.Context(), tx, brand, id, now); err != nil {
			return err
		}
		return wipeIfUnused(r.Context(), tx, brand, c.GrantID, now)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}
