// Package core holds workspace, brand and role rules as pure functions:
// the permission matrix, name validation, and invitation rules.
package core

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode"
	"unicode/utf8"
	"uuid"
)

const maxNameLen = 100

var (
	ErrInvalidRole       = errors.New("workspace: invalid role")
	ErrInvalidName       = errors.New("workspace: name must be 1-100 printable characters")
	ErrInvalidPrincipal  = errors.New("workspace: invalid principal")
	ErrForbidden         = errors.New("workspace: forbidden")
	ErrInvalidInvitation = errors.New("workspace: an invitation needs one or more distinct brands")
	ErrEmailMismatch     = errors.New("workspace: invitation is for a different or unverified email")
)

// Role is a per-brand role. The workspace owner is not a role: ownership
// is workspace-wide and implies every permission.
type Role string

const (
	RoleAdmin     Role = "admin"
	RolePublisher Role = "publisher"
	RoleAgent     Role = "agent"
	RoleViewer    Role = "viewer"
)

// ParseRole validates a role name.
func ParseRole(s string) (Role, error) {
	switch r := Role(s); r {
	case RoleAdmin, RolePublisher, RoleAgent, RoleViewer:
		return r, nil
	}
	return "", ErrInvalidRole
}

// Permission is an action checked by Can.
type Permission string

// Workspace-wide permissions (owner only).
const (
	PermWorkspaceManage Permission = "workspace.manage"
	PermBrandCreate     Permission = "brand.create"
	PermAuditRead       Permission = "audit.read"
)

// Brand-scoped permissions.
const (
	PermBrandRead          Permission = "brand.read"
	PermBrandManage        Permission = "brand.manage"
	PermBrandMembersManage Permission = "brand.members.manage"
	PermPostCreate         Permission = "post.create"
	PermPostPublish        Permission = "post.publish"
	PermInboxRead          Permission = "inbox.read"
	PermInboxReply         Permission = "inbox.reply"
	PermAnalyticsRead      Permission = "analytics.read"
	PermConnectionRead     Permission = "connection.read"
	PermConnectionManage   Permission = "connection.manage"
)

// AllPermissions lists every permission.
func AllPermissions() []Permission {
	return []Permission{
		PermWorkspaceManage, PermBrandCreate, PermAuditRead,
		PermBrandRead, PermBrandManage, PermBrandMembersManage, PermPostCreate, PermPostPublish,
		PermInboxRead, PermInboxReply, PermAnalyticsRead, PermConnectionRead, PermConnectionManage,
	}
}

// roleAllows is the brand-level matrix. Every role includes the viewer's
// permissions; admins hold all brand-scoped ones.
func roleAllows(r Role, perm Permission) bool {
	switch perm {
	case PermBrandRead, PermAnalyticsRead, PermConnectionRead:
		return r == RoleViewer || r == RoleAgent || r == RolePublisher || r == RoleAdmin
	case PermInboxRead, PermInboxReply:
		return r == RoleAgent || r == RoleAdmin
	case PermPostCreate, PermPostPublish:
		return r == RolePublisher || r == RoleAdmin
	case PermBrandManage, PermBrandMembersManage, PermConnectionManage:
		return r == RoleAdmin
	}
	return false
}

// Principal is a user acting inside one workspace.
type Principal struct {
	userID      uuid.UUID
	workspaceID uuid.UUID
	isOwner     bool
	brandRoles  map[uuid.UUID]Role
}

// NewPrincipal validates and copies the membership.
func NewPrincipal(user, workspace uuid.UUID, isOwner bool, brandRoles map[uuid.UUID]Role) (Principal, error) {
	if user == uuid.Nil() || workspace == uuid.Nil() {
		return Principal{}, ErrInvalidPrincipal
	}
	for _, r := range brandRoles {
		if _, err := ParseRole(string(r)); err != nil {
			return Principal{}, err
		}
	}
	return Principal{userID: user, workspaceID: workspace, isOwner: isOwner, brandRoles: maps.Clone(brandRoles)}, nil
}

func (p Principal) UserID() uuid.UUID      { return p.userID }
func (p Principal) WorkspaceID() uuid.UUID { return p.workspaceID }
func (p Principal) IsOwner() bool          { return p.isOwner }

// BrandRole returns p's role on brand, if any.
func (p Principal) BrandRole(brand uuid.UUID) (Role, bool) {
	r, ok := p.brandRoles[brand]
	return r, ok
}

// Can reports whether p may perform perm (on brand, for brand-scoped
// permissions) in its workspace.
func Can(p Principal, perm Permission, brand uuid.UUID) bool {
	if p.isOwner {
		return true
	}
	r, ok := p.brandRoles[brand]
	return ok && roleAllows(r, perm)
}

// CanOnAnyBrand reports whether p may perform the brand-scoped perm on at
// least one brand (e.g. whether p administers any brand).
func CanOnAnyBrand(p Principal, perm Permission) bool {
	if p.isOwner {
		return true
	}
	for _, r := range p.brandRoles {
		if roleAllows(r, perm) {
			return true
		}
	}
	return false
}

// ValidateName trims and validates a workspace or brand name.
func ValidateName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxNameLen || strings.ContainsFunc(s, unicode.IsControl) {
		return "", ErrInvalidName
	}
	return s, nil
}

// BrandRole grants role on brand.
type BrandRole struct {
	Brand uuid.UUID `json:"brand_id"`
	Role  Role      `json:"role"`
}

// CheckInvitation validates the grants of an invitation sent by p: every
// brand distinct, every role valid, and p allowed to manage each brand's members.
func CheckInvitation(p Principal, grants []BrandRole) error {
	if len(grants) == 0 {
		return ErrInvalidInvitation
	}
	seen := map[uuid.UUID]bool{}
	for _, g := range grants {
		if _, err := ParseRole(string(g.Role)); err != nil {
			return err
		}
		if seen[g.Brand] {
			return ErrInvalidInvitation
		}
		seen[g.Brand] = true
		if !Can(p, PermBrandMembersManage, g.Brand) {
			return fmt.Errorf("%w: cannot invite to brand %s", ErrForbidden, g.Brand)
		}
	}
	return nil
}

// CheckAcceptance requires the accepting user's verified email to be the invited one.
func CheckAcceptance(invitedEmailNormalized, userEmailNormalized string, userEmailVerified bool) error {
	if !userEmailVerified || invitedEmailNormalized != userEmailNormalized {
		return ErrEmailMismatch
	}
	return nil
}
