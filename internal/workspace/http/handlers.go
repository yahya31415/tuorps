package wshttp

import (
	"errors"
	"io"
	"net/http"
	"net/mail"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/auth"
	authcore "github.com/yahya31415/tuorps/internal/auth/core"
	authhttp "github.com/yahya31415/tuorps/internal/auth/http"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/workspace"
	"github.com/yahya31415/tuorps/internal/workspace/core"
)

type nameRequest struct {
	Name string `json:"name"`
}

func (n nameRequest) Validate() error {
	if _, err := core.ValidateName(n.Name); err != nil {
		return errors.New("name must be 1-100 characters")
	}
	return nil
}

type roleRequest struct {
	Role string `json:"role"`
}

func (r roleRequest) Validate() error {
	if _, err := core.ParseRole(r.Role); err != nil {
		return errors.New("role must be admin, publisher, agent or viewer")
	}
	return nil
}

type invitationRequest struct {
	Email  string           `json:"email"`
	Grants []core.BrandRole `json:"grants"`
}

func (i invitationRequest) Validate() error {
	if _, _, err := authcore.NormalizeEmail(i.Email); err != nil {
		return errors.New("email must be a single valid address")
	}
	if len(i.Grants) == 0 || len(i.Grants) > 100 {
		return errors.New("grants must list 1-100 brand roles")
	}
	seen := map[uuid.UUID]bool{}
	for _, g := range i.Grants {
		if _, err := core.ParseRole(string(g.Role)); err != nil {
			return errors.New("role must be admin, publisher, agent or viewer")
		}
		if seen[g.Brand] {
			return errors.New("each brand may appear once")
		}
		seen[g.Brand] = true
	}
	return nil
}

type acceptRequest struct {
	Token string `json:"token"`
}

func (a acceptRequest) Validate() error {
	if a.Token == "" || len(a.Token) > 128 {
		return errors.New("token is required")
	}
	return nil
}

type workspaceJSON struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	IsOwner   bool      `json:"is_owner"`
	CreatedAt time.Time `json:"created_at"`
}

type brandJSON struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Archived  bool      `json:"archived"`
	CreatedAt time.Time `json:"created_at"`
}

func toBrand(b workspace.Brand) brandJSON {
	return brandJSON{ID: b.ID, Name: b.Name, Archived: b.Archived, CreatedAt: b.CreatedAt}
}

type memberJSON struct {
	UserID   uuid.UUID        `json:"user_id"`
	Name     string           `json:"name"`
	Email    string           `json:"email"`
	IsOwner  bool             `json:"is_owner"`
	JoinedAt time.Time        `json:"joined_at"`
	Roles    []core.BrandRole `json:"roles"`
}

type invitationJSON struct {
	ID        uuid.UUID        `json:"id"`
	Email     string           `json:"email"`
	Grants    []core.BrandRole `json:"grants"`
	InvitedBy uuid.UUID        `json:"invited_by"`
	ExpiresAt time.Time        `json:"expires_at"`
	CreatedAt time.Time        `json:"created_at"`
}

type auditJSON struct {
	ID          uuid.UUID         `json:"id"`
	ActorUserID *uuid.UUID        `json:"actor_user_id"`
	Action      string            `json:"action"`
	TargetType  string            `json:"target_type"`
	TargetID    string            `json:"target_id"`
	RequestID   string            `json:"request_id"`
	Details     map[string]string `json:"details"`
	At          time.Time         `json:"at"`
}

func (h *handlers) listMine(w http.ResponseWriter, r *http.Request) {
	id, _ := authhttp.IdentityFrom(r.Context())
	pg, ok := page(w, r)
	if !ok {
		return
	}
	var items []workspaceJSON
	if !h.read(w, r, postgres.Scope{UserID: id.UserID}, func(tx pgx.Tx) error {
		rows, err := workspace.ListMyWorkspaces(r.Context(), tx, id.UserID, pg.After, pg.Limit)
		for _, s := range rows {
			items = append(items, workspaceJSON{ID: s.ID, Name: s.Name, IsOwner: s.IsOwner, CreatedAt: s.CreatedAt})
		}
		return err
	}) {
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, newList(items, pg.Limit, func(x workspaceJSON) uuid.UUID { return x.ID }))
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	id, _ := authhttp.IdentityFrom(r.Context())
	req, ok := httpx.Decode[nameRequest](w, r, maxBody)
	if !ok {
		return
	}
	name, _ := core.ValidateName(req.Name)
	ws, brand, now := h.NewID(), h.NewID(), h.Now()
	err := h.Writer.InWriteTx(r.Context(), postgres.Scope{WorkspaceID: ws, UserID: id.UserID}, event("workspace.create", "workspace", ws, nil), h.meta(r), func(tx audit.Tx) error {
		return workspace.Bootstrap(r.Context(), tx, workspace.NewWorkspace{ID: ws, Name: name, Owner: id.UserID, BrandID: brand, BrandName: name, Now: now})
	})
	if err != nil {
		h.internalError(w, r, "workspace.create", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, workspaceJSON{ID: ws, Name: name, IsOwner: true, CreatedAt: now})
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var ws workspace.Workspace
	if !h.read(w, r, scopeOf(p), func(tx pgx.Tx) error {
		var err error
		ws, err = workspace.GetWorkspace(r.Context(), tx, p.WorkspaceID())
		return err
	}) {
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, workspaceJSON{ID: ws.ID, Name: ws.Name, IsOwner: p.IsOwner(), CreatedAt: ws.CreatedAt})
}

func (h *handlers) rename(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if !core.Can(p, core.PermWorkspaceManage, uuid.Nil()) {
		forbidden(w, r)
		return
	}
	req, ok := httpx.Decode[nameRequest](w, r, maxBody)
	if !ok {
		return
	}
	name, _ := core.ValidateName(req.Name)
	if h.write(w, r, p, event("workspace.update", "workspace", p.WorkspaceID(), map[string]string{"name": name}), func(tx audit.Tx) error {
		return workspace.RenameWorkspace(r.Context(), tx, name, h.Now())
	}) {
		h.get(w, r)
	}
}

func (h *handlers) listBrands(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	pg, ok := page(w, r)
	if !ok {
		return
	}
	var items []brandJSON
	if !h.read(w, r, scopeOf(p), func(tx pgx.Tx) error {
		rows, err := workspace.ListBrands(r.Context(), tx, p, pg.After, pg.Limit)
		for _, b := range rows {
			items = append(items, toBrand(b))
		}
		return err
	}) {
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, newList(items, pg.Limit, func(b brandJSON) uuid.UUID { return b.ID }))
}

func (h *handlers) createBrand(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if !core.Can(p, core.PermBrandCreate, uuid.Nil()) {
		forbidden(w, r)
		return
	}
	req, ok := httpx.Decode[nameRequest](w, r, maxBody)
	if !ok {
		return
	}
	name, _ := core.ValidateName(req.Name)
	id, now := h.NewID(), h.Now()
	if h.write(w, r, p, event("brand.create", "brand", id, map[string]string{"name": name}), func(tx audit.Tx) error {
		return workspace.CreateBrand(r.Context(), tx, id, name, now)
	}) {
		_ = httpx.WriteJSON(w, http.StatusCreated, brandJSON{ID: id, Name: name, CreatedAt: now})
	}
}

// brandFor resolves {brand} for p: unknown, foreign or unreadable brands are 404.
func (h *handlers) brandFor(w http.ResponseWriter, r *http.Request, p core.Principal) (workspace.Brand, bool) {
	id, ok := pathID(r, "brand")
	if !ok || !core.Can(p, core.PermBrandRead, id) {
		notFound(w, r)
		return workspace.Brand{}, false
	}
	var b workspace.Brand
	ok = h.read(w, r, scopeOf(p), func(tx pgx.Tx) error {
		var err error
		b, err = workspace.GetBrand(r.Context(), tx, p.WorkspaceID(), id)
		return err
	})
	return b, ok
}

func (h *handlers) getBrand(w http.ResponseWriter, r *http.Request) {
	if b, ok := h.brandFor(w, r, principalFrom(r.Context())); ok {
		_ = httpx.WriteJSON(w, http.StatusOK, toBrand(b))
	}
}

func (h *handlers) renameBrand(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	b, ok := h.brandFor(w, r, p)
	if !ok {
		return
	}
	if !core.Can(p, core.PermBrandManage, b.ID) {
		forbidden(w, r)
		return
	}
	req, ok := httpx.Decode[nameRequest](w, r, maxBody)
	if !ok {
		return
	}
	name, _ := core.ValidateName(req.Name)
	if h.write(w, r, p, event("brand.update", "brand", b.ID, map[string]string{"name": name}), func(tx audit.Tx) error {
		return workspace.RenameBrand(r.Context(), tx, b.ID, name, h.Now())
	}) {
		b.Name = name
		_ = httpx.WriteJSON(w, http.StatusOK, toBrand(b))
	}
}

func (h *handlers) archiveBrand(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	b, ok := h.brandFor(w, r, p)
	if !ok {
		return
	}
	if !core.Can(p, core.PermWorkspaceManage, uuid.Nil()) { // brands are billed: owner only
		forbidden(w, r)
		return
	}
	if h.write(w, r, p, event("brand.archive", "brand", b.ID, nil), func(tx audit.Tx) error {
		return workspace.ArchiveBrand(r.Context(), tx, b.ID, h.Now())
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// memberTarget resolves {brand}/{user} for role changes by p.
func (h *handlers) memberTarget(w http.ResponseWriter, r *http.Request, p core.Principal) (brand, user uuid.UUID, ok bool) {
	b, ok := h.brandFor(w, r, p)
	if !ok {
		return uuid.Nil(), uuid.Nil(), false
	}
	if !core.Can(p, core.PermBrandMembersManage, b.ID) {
		forbidden(w, r)
		return uuid.Nil(), uuid.Nil(), false
	}
	u, ok := pathID(r, "user")
	if !ok {
		notFound(w, r)
		return uuid.Nil(), uuid.Nil(), false
	}
	return b.ID, u, true
}

func (h *handlers) setRole(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brand, user, ok := h.memberTarget(w, r, p)
	if !ok {
		return
	}
	req, ok := httpx.Decode[roleRequest](w, r, maxBody)
	if !ok {
		return
	}
	role, _ := core.ParseRole(req.Role)
	if h.write(w, r, p, event("member.role_set", "user", user, map[string]string{"brand_id": brand.String(), "role": string(role)}), func(tx audit.Tx) error {
		return workspace.SetBrandRole(r.Context(), tx, brand, user, role, h.Now())
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *handlers) removeRole(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brand, user, ok := h.memberTarget(w, r, p)
	if !ok {
		return
	}
	if h.write(w, r, p, event("member.role_remove", "user", user, map[string]string{"brand_id": brand.String()}), func(tx audit.Tx) error {
		return workspace.RemoveBrandRole(r.Context(), tx, brand, user)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *handlers) listMembers(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if !requireMemberManager(w, r, p) {
		return
	}
	pg, ok := page(w, r)
	if !ok {
		return
	}
	var items []memberJSON
	if !h.read(w, r, scopeOf(p), func(tx pgx.Tx) error {
		rows, err := workspace.ListMembers(r.Context(), tx, p.WorkspaceID(), pg.After, pg.Limit)
		for _, m := range rows {
			items = append(items, memberJSON{UserID: m.UserID, Name: m.DisplayName, Email: m.Email, IsOwner: m.IsOwner, JoinedAt: m.JoinedAt, Roles: m.Roles})
		}
		return err
	}) {
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, newList(items, pg.Limit, func(m memberJSON) uuid.UUID { return m.UserID }))
}

func (h *handlers) removeMember(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if !core.Can(p, core.PermWorkspaceManage, uuid.Nil()) {
		forbidden(w, r)
		return
	}
	user, ok := pathID(r, "user")
	if !ok {
		notFound(w, r)
		return
	}
	if h.write(w, r, p, event("member.remove", "user", user, nil), func(tx audit.Tx) error {
		return workspace.RemoveMember(r.Context(), tx, user)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// requireMemberManager writes 403 unless p is the owner or administers a brand.
func requireMemberManager(w http.ResponseWriter, r *http.Request, p core.Principal) bool {
	if !core.CanOnAnyBrand(p, core.PermBrandMembersManage) {
		forbidden(w, r)
		return false
	}
	return true
}

func (h *handlers) listInvitations(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if !requireMemberManager(w, r, p) {
		return
	}
	pg, ok := page(w, r)
	if !ok {
		return
	}
	var items []invitationJSON
	if !h.read(w, r, scopeOf(p), func(tx pgx.Tx) error {
		rows, err := workspace.ListInvitations(r.Context(), tx, p.WorkspaceID(), pg.After, pg.Limit)
		for _, i := range rows {
			items = append(items, invitationJSON{ID: i.ID, Email: i.EmailNormalized, Grants: i.Grants, InvitedBy: i.InvitedBy, ExpiresAt: i.ExpiresAt, CreatedAt: i.CreatedAt})
		}
		return err
	}) {
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, newList(items, pg.Limit, func(i invitationJSON) uuid.UUID { return i.ID }))
}

func (h *handlers) createInvitation(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	req, ok := httpx.Decode[invitationRequest](w, r, maxBody)
	if !ok {
		return
	}
	switch err := core.CheckInvitation(p, req.Grants); {
	case errors.Is(err, core.ErrForbidden):
		forbidden(w, r)
		return
	case err != nil:
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		return
	}
	_, normalized, _ := authcore.NormalizeEmail(req.Email)
	raw := make([]byte, 32)
	if _, err := io.ReadFull(h.Rand, raw); err != nil {
		h.internalError(w, r, "invitation_token", err)
		return
	}
	token, _ := authcore.EncodeToken(raw)
	hash, _ := authcore.HashToken(token)
	id, now := h.NewID(), h.Now()
	inv := workspace.NewInvitation{ID: id, EmailNormalized: normalized, Grants: req.Grants, InvitedBy: p.UserID(), TokenHash: hash, ExpiresAt: now.Add(invitationTTL), Now: now}
	err := h.Writer.InWriteTx(r.Context(), scopeOf(p), event("invitation.create", "invitation", id, map[string]string{"email_domain": domainOf(normalized)}), h.meta(r), func(tx audit.Tx) error {
		if err := workspace.CreateInvitation(r.Context(), tx, inv); err != nil {
			return err
		}
		ws, err := workspace.GetWorkspace(r.Context(), tx.Pgx(), p.WorkspaceID())
		if err != nil {
			return err
		}
		inviter, err := auth.GetProfile(r.Context(), tx.Pgx(), p.UserID())
		if err != nil {
			return err
		}
		m, err := emailcore.NewMessage(mail.Address{Address: normalized}, emailcore.KindInvitation, "en", map[string]string{
			"inviter": inviter.DisplayName, "workspace": ws.Name,
			"link": emailcore.Link(h.Origin, "/accept-invitation", map[string]string{"token": token}),
		})
		if err != nil {
			return err
		}
		return h.Mailer.Enqueue(r.Context(), tx, m)
	})
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, "unknown_brand", "every brand must be an active brand of this workspace")
	case err != nil:
		h.internalError(w, r, "invitation.create", err)
	default:
		_ = httpx.WriteJSON(w, http.StatusCreated, invitationJSON{ID: id, Email: normalized, Grants: req.Grants, InvitedBy: p.UserID(), ExpiresAt: inv.ExpiresAt, CreatedAt: now})
	}
}

func domainOf(email string) string {
	for i := len(email) - 1; i >= 0; i-- {
		if email[i] == '@' {
			return email[i+1:]
		}
	}
	return ""
}

func (h *handlers) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if !requireMemberManager(w, r, p) {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w, r)
		return
	}
	if h.write(w, r, p, event("invitation.revoke", "invitation", id, nil), func(tx audit.Tx) error {
		return workspace.RevokeInvitation(r.Context(), tx, id, h.Now())
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

var (
	errInvalidToken  = errors.New("invalid or expired invitation")
	errEmailMismatch = errors.New("this invitation is for a different email address; sign in with that address (verified) to accept it")
)

// accept joins the caller to the invitation's workspace with its roles.
func (h *handlers) accept(w http.ResponseWriter, r *http.Request) {
	id, _ := authhttp.IdentityFrom(r.Context())
	req, ok := httpx.Decode[acceptRequest](w, r, maxBody)
	if !ok {
		return
	}
	hash, valid := authcore.HashToken(req.Token)
	if !valid {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_token", errInvalidToken.Error())
		return
	}
	now := h.Now()
	var inv workspace.PendingInvitation
	err := h.Writer.InWriteTxResolving(r.Context(),
		func(tx pgx.Tx) (postgres.Scope, error) {
			var found bool
			var err error
			inv, found, err = workspace.FindInvitation(r.Context(), tx, hash, now)
			if err != nil || !found {
				return postgres.Scope{}, errors.Join(err, errInvalidToken)
			}
			return postgres.Scope{WorkspaceID: inv.WorkspaceID, UserID: id.UserID}, nil
		},
		func(s postgres.Scope) (auditcore.Event, error) {
			return event("invitation.accept", "invitation", inv.ID, nil), nil
		},
		h.meta(r),
		func(tx audit.Tx) error {
			profile, err := auth.GetProfile(r.Context(), tx.Pgx(), id.UserID)
			if err != nil {
				return err
			}
			_, normalized, _ := authcore.NormalizeEmail(profile.Email)
			if err := core.CheckAcceptance(inv.EmailNormalized, normalized, profile.EmailVerified); err != nil {
				return errEmailMismatch
			}
			if err := workspace.AcceptInvitation(r.Context(), tx, inv, id.UserID, now); errors.Is(err, workspace.ErrNotFound) {
				return errInvalidToken
			} else if err != nil {
				return err
			}
			return nil
		})
	switch {
	case errors.Is(err, errInvalidToken):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_token", errInvalidToken.Error())
	case errors.Is(err, errEmailMismatch):
		httpx.WriteError(w, r, http.StatusForbidden, "invitation_email_mismatch", errEmailMismatch.Error())
	case err != nil:
		h.internalError(w, r, "invitation.accept", err)
	default:
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]uuid.UUID{"workspace_id": inv.WorkspaceID})
	}
}

func (h *handlers) auditLog(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if !core.Can(p, core.PermAuditRead, uuid.Nil()) {
		forbidden(w, r)
		return
	}
	pg, ok := page(w, r)
	if !ok {
		return
	}
	recs, err := audit.ListWorkspace(r.Context(), h.Pool, scopeOf(p), pg.After, pg.Limit)
	if err != nil {
		h.internalError(w, r, "audit_log", err)
		return
	}
	items := make([]auditJSON, len(recs))
	for i, rec := range recs {
		items[i] = auditJSON{ID: rec.ID, ActorUserID: rec.ActorUserID, Action: rec.Action, TargetType: rec.TargetType, TargetID: rec.TargetID, RequestID: rec.RequestID, Details: rec.Details, At: rec.At}
	}
	_ = httpx.WriteJSON(w, http.StatusOK, newList(items, pg.Limit, func(a auditJSON) uuid.UUID { return a.ID }))
}
