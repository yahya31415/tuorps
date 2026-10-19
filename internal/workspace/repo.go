// Package workspace is the shell for workspaces, brands, members, roles and
// invitations: the sqlc-backed repository and the HTTP handlers. Rules live
// in workspace/core.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/workspace/adapters/postgres/sqlcgen"
	"github.com/yahya31415/tuorps/internal/workspace/core"
)

// Repository errors. ErrNotFound also covers rows hidden by row-level
// security, so another workspace's data is indistinguishable from absent data.
var (
	ErrNotFound  = errors.New("workspace: not found")
	ErrNameTaken = errors.New("workspace: a brand with this name already exists")
	ErrOwner     = errors.New("workspace: the owner cannot be removed")
)

// NewWorkspace is what Bootstrap creates.
type NewWorkspace struct {
	ID        uuid.UUID
	Name      string
	Owner     uuid.UUID
	BrandID   uuid.UUID
	BrandName string
	Now       time.Time
}

// Workspace is a workspace's public data.
type Workspace struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
}

// WorkspaceSummary is one row of "my workspaces".
type WorkspaceSummary struct {
	ID        uuid.UUID
	Name      string
	IsOwner   bool
	CreatedAt time.Time
}

// Brand is a brand's public data.
type Brand struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
	Archived  bool
}

// Member is a workspace member with their brand roles.
type Member struct {
	UserID      uuid.UUID
	DisplayName string
	Email       string
	IsOwner     bool
	JoinedAt    time.Time
	Roles       []core.BrandRole
}

// NewInvitation is an invitation to create.
type NewInvitation struct {
	ID              uuid.UUID
	EmailNormalized string
	Grants          []core.BrandRole
	InvitedBy       uuid.UUID
	TokenHash       [32]byte
	ExpiresAt       time.Time
	Now             time.Time
}

// Invitation is a pending invitation as listed to its workspace.
type Invitation struct {
	ID              uuid.UUID
	EmailNormalized string
	Grants          []core.BrandRole
	InvitedBy       uuid.UUID
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

// PendingInvitation is an invitation found by its token.
type PendingInvitation struct {
	ID              uuid.UUID
	WorkspaceID     uuid.UUID
	EmailNormalized string
	Grants          []core.BrandRole
	ExpiresAt       time.Time
}

func q(tx pgx.Tx) *sqlcgen.Queries { return sqlcgen.New(tx) }

func affected(n int64, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// mapErr turns constraint violations into domain errors.
func mapErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "brands_active_name":
			return ErrNameTaken
		case pgErr.Code == "23503":
			return ErrNotFound
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Bootstrap creates a workspace owned by nw.Owner with its first brand. The
// transaction's scope must be {nw.ID, nw.Owner}.
func Bootstrap(ctx context.Context, tx audit.Tx, nw NewWorkspace) error {
	if err := q(tx.Pgx()).InsertWorkspace(ctx, sqlcgen.InsertWorkspaceParams{ID: nw.ID, Name: nw.Name, CreatedBy: nw.Owner, Now: nw.Now}); err != nil {
		return mapErr(err)
	}
	if _, err := q(tx.Pgx()).InsertMember(ctx, sqlcgen.InsertMemberParams{WorkspaceID: nw.ID, UserID: nw.Owner, IsOwner: true, Now: nw.Now}); err != nil {
		return mapErr(err)
	}
	return mapErr(q(tx.Pgx()).InsertBrand(ctx, sqlcgen.InsertBrandParams{ID: nw.BrandID, WorkspaceID: nw.ID, Name: nw.BrandName, Now: nw.Now}))
}

// LoadPrincipal builds the scoped user's principal in the scoped workspace.
func LoadPrincipal(ctx context.Context, tx pgx.Tx, s postgres.Scope) (core.Principal, error) {
	owner, err := q(tx).Membership(ctx, sqlcgen.MembershipParams{WorkspaceID: s.WorkspaceID, UserID: s.UserID})
	if err != nil {
		return core.Principal{}, mapErr(err)
	}
	rows, err := q(tx).MemberBrandRoles(ctx, sqlcgen.MemberBrandRolesParams{WorkspaceID: s.WorkspaceID, UserID: s.UserID})
	if err != nil {
		return core.Principal{}, err
	}
	roles := make(map[uuid.UUID]core.Role, len(rows))
	for _, r := range rows {
		roles[r.BrandID] = core.Role(r.Role)
	}
	return core.NewPrincipal(s.UserID, s.WorkspaceID, owner, roles)
}

// ListMyWorkspaces pages the user's memberships by workspace id.
func ListMyWorkspaces(ctx context.Context, tx pgx.Tx, user, after uuid.UUID, limit int32) ([]WorkspaceSummary, error) {
	rows, err := q(tx).ListMyWorkspaces(ctx, sqlcgen.ListMyWorkspacesParams{UserID: user, After: after, PageSize: limit})
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceSummary, len(rows))
	for i, r := range rows {
		out[i] = WorkspaceSummary{ID: r.ID, Name: r.Name, IsOwner: r.IsOwner, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

// GetWorkspace returns a visible workspace.
func GetWorkspace(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Workspace, error) {
	r, err := q(tx).GetWorkspace(ctx, id)
	if err != nil {
		return Workspace{}, mapErr(err)
	}
	return Workspace{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt}, nil
}

// RenameWorkspace renames the scoped workspace.
func RenameWorkspace(ctx context.Context, tx audit.Tx, name string, now time.Time) error {
	n, err := q(tx.Pgx()).RenameWorkspace(ctx, sqlcgen.RenameWorkspaceParams{ID: tx.Scope().WorkspaceID, Name: name, Now: now})
	return affected(n, err)
}

// CreateBrand adds a brand to the scoped workspace.
func CreateBrand(ctx context.Context, tx audit.Tx, id uuid.UUID, name string, now time.Time) error {
	return mapErr(q(tx.Pgx()).InsertBrand(ctx, sqlcgen.InsertBrandParams{ID: id, WorkspaceID: tx.Scope().WorkspaceID, Name: name, Now: now}))
}

// ListBrands pages the brands p may see: all for the owner, otherwise
// those p holds a role on.
func ListBrands(ctx context.Context, tx pgx.Tx, p core.Principal, after uuid.UUID, limit int32) ([]Brand, error) {
	var out []Brand
	if p.IsOwner() {
		rows, err := q(tx).ListBrands(ctx, sqlcgen.ListBrandsParams{WorkspaceID: p.WorkspaceID(), After: after, PageSize: limit})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, Brand{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, Archived: r.ArchivedAt != nil})
		}
		return out, nil
	}
	rows, err := q(tx).ListBrandsForMember(ctx, sqlcgen.ListBrandsForMemberParams{WorkspaceID: p.WorkspaceID(), UserID: p.UserID(), After: after, PageSize: limit})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, Brand{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, Archived: r.ArchivedAt != nil})
	}
	return out, nil
}

// GetBrand returns a brand of workspace ws.
func GetBrand(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) (Brand, error) {
	r, err := q(tx).GetBrand(ctx, sqlcgen.GetBrandParams{WorkspaceID: ws, ID: id})
	if err != nil {
		return Brand{}, mapErr(err)
	}
	return Brand{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, Archived: r.ArchivedAt != nil}, nil
}

// RenameBrand renames an active brand of the scoped workspace.
func RenameBrand(ctx context.Context, tx audit.Tx, id uuid.UUID, name string, now time.Time) error {
	n, err := q(tx.Pgx()).RenameBrand(ctx, sqlcgen.RenameBrandParams{WorkspaceID: tx.Scope().WorkspaceID, ID: id, Name: name, Now: now})
	return affected(n, err)
}

// ArchiveBrand archives an active brand of the scoped workspace.
func ArchiveBrand(ctx context.Context, tx audit.Tx, id uuid.UUID, now time.Time) error {
	n, err := q(tx.Pgx()).ArchiveBrand(ctx, sqlcgen.ArchiveBrandParams{WorkspaceID: tx.Scope().WorkspaceID, ID: id, Now: &now})
	return affected(n, err)
}

// ListMembers pages workspace ws's members with their brand roles.
func ListMembers(ctx context.Context, tx pgx.Tx, ws, after uuid.UUID, limit int32) ([]Member, error) {
	rows, err := q(tx).ListMembers(ctx, sqlcgen.ListMembersParams{WorkspaceID: ws, After: after, PageSize: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Member, len(rows))
	idx := make(map[uuid.UUID]int, len(rows))
	userIDs := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = Member{UserID: r.UserID, DisplayName: r.DisplayName, Email: r.Email, IsOwner: r.IsOwner, JoinedAt: r.CreatedAt, Roles: []core.BrandRole{}}
		idx[r.UserID], userIDs[i] = i, r.UserID
	}
	roles, err := q(tx).RolesForMembers(ctx, sqlcgen.RolesForMembersParams{WorkspaceID: ws, UserIds: userIDs})
	if err != nil {
		return nil, err
	}
	for _, r := range roles {
		m := &out[idx[r.UserID]]
		m.Roles = append(m.Roles, core.BrandRole{Brand: r.BrandID, Role: core.Role(r.Role)})
	}
	return out, nil
}

// RemoveMember removes a non-owner from the scoped workspace (and their roles).
func RemoveMember(ctx context.Context, tx audit.Tx, user uuid.UUID) error {
	ws := tx.Scope().WorkspaceID
	owner, err := q(tx.Pgx()).IsOwner(ctx, sqlcgen.IsOwnerParams{WorkspaceID: ws, UserID: user})
	if err != nil {
		return mapErr(err)
	}
	if owner {
		return ErrOwner
	}
	n, err := q(tx.Pgx()).RemoveMember(ctx, sqlcgen.RemoveMemberParams{WorkspaceID: ws, UserID: user})
	return affected(n, err)
}

// SetBrandRole grants (or changes) a member's role on a brand.
func SetBrandRole(ctx context.Context, tx audit.Tx, brand, user uuid.UUID, role core.Role, now time.Time) error {
	return mapErr(q(tx.Pgx()).SetBrandRole(ctx, sqlcgen.SetBrandRoleParams{
		WorkspaceID: tx.Scope().WorkspaceID, BrandID: brand, UserID: user, Role: string(role), Now: now,
	}))
}

// RemoveBrandRole revokes a member's role on a brand.
func RemoveBrandRole(ctx context.Context, tx audit.Tx, brand, user uuid.UUID) error {
	n, err := q(tx.Pgx()).RemoveBrandRole(ctx, sqlcgen.RemoveBrandRoleParams{WorkspaceID: tx.Scope().WorkspaceID, BrandID: brand, UserID: user})
	return affected(n, err)
}

// CreateInvitation stores an invitation; every granted brand must be an
// active brand of the scoped workspace.
func CreateInvitation(ctx context.Context, tx audit.Tx, inv NewInvitation) error {
	ws := tx.Scope().WorkspaceID
	brands := make([]uuid.UUID, len(inv.Grants))
	for i, g := range inv.Grants {
		brands[i] = g.Brand
	}
	n, err := q(tx.Pgx()).CountActiveBrands(ctx, sqlcgen.CountActiveBrandsParams{WorkspaceID: ws, Ids: brands})
	if err != nil {
		return err
	}
	if int(n) != len(brands) {
		return ErrNotFound
	}
	grants, err := json.Marshal(inv.Grants)
	if err != nil {
		return fmt.Errorf("workspace: grants: %w", err)
	}
	return mapErr(q(tx.Pgx()).InsertInvitation(ctx, sqlcgen.InsertInvitationParams{
		ID: inv.ID, WorkspaceID: ws, EmailNormalized: inv.EmailNormalized, BrandRoles: grants,
		InvitedBy: inv.InvitedBy, TokenHash: inv.TokenHash[:], ExpiresAt: inv.ExpiresAt, Now: inv.Now,
	}))
}

// ListInvitations pages workspace ws's pending invitations, newest first.
func ListInvitations(ctx context.Context, tx pgx.Tx, ws, before uuid.UUID, limit int32) ([]Invitation, error) {
	if before == uuid.Nil() {
		before = uuid.Max()
	}
	rows, err := q(tx).ListInvitations(ctx, sqlcgen.ListInvitationsParams{WorkspaceID: ws, Before: before, PageSize: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Invitation, 0, len(rows))
	for _, r := range rows {
		var g []core.BrandRole
		if err := json.Unmarshal(r.BrandRoles, &g); err != nil {
			return nil, fmt.Errorf("workspace: grants: %w", err)
		}
		out = append(out, Invitation{ID: r.ID, EmailNormalized: r.EmailNormalized, Grants: g, InvitedBy: r.InvitedBy, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// RevokeInvitation withdraws a pending invitation of the scoped workspace.
func RevokeInvitation(ctx context.Context, tx audit.Tx, id uuid.UUID, now time.Time) error {
	n, err := q(tx.Pgx()).RevokeInvitation(ctx, sqlcgen.RevokeInvitationParams{WorkspaceID: tx.Scope().WorkspaceID, ID: id, Now: &now})
	return affected(n, err)
}

// FindInvitation looks up a pending invitation by token hash (no tenant scope).
func FindInvitation(ctx context.Context, tx pgx.Tx, hash [32]byte, now time.Time) (PendingInvitation, bool, error) {
	r, err := q(tx).FindInvitation(ctx, sqlcgen.FindInvitationParams{TokenHash: hash[:], Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingInvitation{}, false, nil
	}
	if err != nil {
		return PendingInvitation{}, false, err
	}
	var g []core.BrandRole
	if err := json.Unmarshal(r.BrandRoles, &g); err != nil {
		return PendingInvitation{}, false, fmt.Errorf("workspace: grants: %w", err)
	}
	return PendingInvitation{ID: r.ID, WorkspaceID: r.WorkspaceID, EmailNormalized: r.EmailNormalized, Grants: g, ExpiresAt: r.ExpiresAt}, true, nil
}

// AcceptInvitation makes user a member with the invitation's roles. The
// transaction's scope must be {inv.WorkspaceID, user}; a second acceptance
// (or a race with revocation) is ErrNotFound.
func AcceptInvitation(ctx context.Context, tx audit.Tx, inv PendingInvitation, user uuid.UUID, now time.Time) error {
	n, err := q(tx.Pgx()).MarkInvitationAccepted(ctx, sqlcgen.MarkInvitationAcceptedParams{WorkspaceID: inv.WorkspaceID, ID: inv.ID, UserID: &user, Now: &now})
	if err := affected(n, err); err != nil {
		return err
	}
	if _, err := q(tx.Pgx()).InsertMember(ctx, sqlcgen.InsertMemberParams{WorkspaceID: inv.WorkspaceID, UserID: user, IsOwner: false, Now: now}); err != nil {
		return mapErr(err)
	}
	for _, g := range inv.Grants {
		if err := SetBrandRole(ctx, tx, g.Brand, user, g.Role, now); err != nil {
			return err
		}
	}
	return nil
}
