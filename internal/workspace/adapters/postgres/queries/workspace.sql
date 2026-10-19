-- All queries run under row-level security as tuorps_app with the tenant
-- GUCs set, and also filter on workspace_id explicitly (defence in depth).

-- name: InsertWorkspace :exec
INSERT INTO workspaces (id, name, created_by, created_at, updated_at) VALUES (@id, @name, @created_by, @now, @now);

-- name: InsertMember :execrows
INSERT INTO workspace_members (workspace_id, user_id, is_owner, created_at)
VALUES (@workspace_id, @user_id, @is_owner, @now) ON CONFLICT DO NOTHING;

-- name: InsertBrand :exec
INSERT INTO brands (id, workspace_id, name, created_at, updated_at) VALUES (@id, @workspace_id, @name, @now, @now);

-- name: Membership :one
SELECT is_owner FROM workspace_members WHERE workspace_id = @workspace_id AND user_id = @user_id;

-- name: MemberBrandRoles :many
SELECT brand_id, role FROM brand_roles WHERE workspace_id = @workspace_id AND user_id = @user_id;

-- name: ListMyWorkspaces :many
-- Keyset by workspace id. Index: workspace_members_user (user_id, workspace_id).
SELECT w.id, w.name, m.is_owner, w.created_at
FROM workspace_members m JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = @user_id AND m.workspace_id > @after
ORDER BY m.workspace_id
LIMIT @page_size;

-- name: GetWorkspace :one
SELECT id, name, created_at FROM workspaces WHERE id = @id;

-- name: RenameWorkspace :execrows
UPDATE workspaces SET name = @name, updated_at = @now WHERE id = @id;

-- name: ListBrands :many
-- Keyset by id. Index: brands_workspace_id (workspace_id, id).
SELECT id, name, created_at, archived_at FROM brands
WHERE workspace_id = @workspace_id AND id > @after
ORDER BY id LIMIT @page_size;

-- name: ListBrandsForMember :many
SELECT b.id, b.name, b.created_at, b.archived_at
FROM brands b JOIN brand_roles r ON r.brand_id = b.id AND r.workspace_id = b.workspace_id
WHERE b.workspace_id = @workspace_id AND r.user_id = @user_id AND b.id > @after
ORDER BY b.id LIMIT @page_size;

-- name: GetBrand :one
SELECT id, name, created_at, archived_at FROM brands WHERE workspace_id = @workspace_id AND id = @id;

-- name: RenameBrand :execrows
UPDATE brands SET name = @name, updated_at = @now
WHERE workspace_id = @workspace_id AND id = @id AND archived_at IS NULL;

-- name: ArchiveBrand :execrows
UPDATE brands SET archived_at = @now, updated_at = @now
WHERE workspace_id = @workspace_id AND id = @id AND archived_at IS NULL;

-- name: CountActiveBrands :one
SELECT count(*) FROM brands WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[]) AND archived_at IS NULL;

-- name: ListMembers :many
-- Keyset by user id. Index: workspace_members_pkey (workspace_id, user_id).
SELECT m.user_id, u.display_name, u.email, m.is_owner, m.created_at
FROM workspace_members m JOIN users u ON u.id = m.user_id
WHERE m.workspace_id = @workspace_id AND m.user_id > @after
ORDER BY m.user_id LIMIT @page_size;

-- name: RolesForMembers :many
SELECT user_id, brand_id, role FROM brand_roles
WHERE workspace_id = @workspace_id AND user_id = ANY(@user_ids::uuid[])
ORDER BY user_id, brand_id;

-- name: RemoveMember :execrows
DELETE FROM workspace_members WHERE workspace_id = @workspace_id AND user_id = @user_id AND NOT is_owner;

-- name: IsOwner :one
SELECT is_owner FROM workspace_members WHERE workspace_id = @workspace_id AND user_id = @user_id;

-- name: SetBrandRole :exec
INSERT INTO brand_roles (workspace_id, brand_id, user_id, role, created_at)
VALUES (@workspace_id, @brand_id, @user_id, @role, @now)
ON CONFLICT (brand_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: RemoveBrandRole :execrows
DELETE FROM brand_roles WHERE workspace_id = @workspace_id AND brand_id = @brand_id AND user_id = @user_id;

-- name: InsertInvitation :exec
INSERT INTO invitations (id, workspace_id, email_normalized, brand_roles, invited_by, token_hash, expires_at, created_at)
VALUES (@id, @workspace_id, @email_normalized, @brand_roles, @invited_by, @token_hash, @expires_at, @now);

-- name: ListInvitations :many
-- Pending invitations, newest first. Index: invitations_workspace (workspace_id, id).
SELECT id, email_normalized, brand_roles, invited_by, expires_at, created_at FROM invitations
WHERE workspace_id = @workspace_id AND id < @before AND accepted_at IS NULL AND revoked_at IS NULL
ORDER BY id DESC LIMIT @page_size;

-- name: RevokeInvitation :execrows
UPDATE invitations SET revoked_at = @now
WHERE workspace_id = @workspace_id AND id = @id AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: FindInvitation :one
SELECT i.id::uuid AS id, i.workspace_id::uuid AS workspace_id, i.email_normalized::text AS email_normalized,
       i.brand_roles::jsonb AS brand_roles, i.expires_at::timestamptz AS expires_at
FROM invitation_by_token(@token_hash, @now) AS i(id, workspace_id, email_normalized, brand_roles, expires_at);

-- name: MarkInvitationAccepted :execrows
UPDATE invitations SET accepted_at = @now, accepted_by = @user_id
WHERE workspace_id = @workspace_id AND id = @id AND accepted_at IS NULL AND revoked_at IS NULL;
