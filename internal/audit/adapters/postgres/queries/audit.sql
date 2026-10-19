-- name: InsertAuditRecord :exec
INSERT INTO audit_log (id, workspace_id, actor_user_id, action, target_type, target_id, request_id, ip_hash, details, at)
VALUES (@id, @workspace_id, @actor_user_id, @action, @target_type, @target_id, @request_id, @ip_hash, @details, @at);

-- name: ListWorkspaceAudit :many
-- Keyset page, newest first. Index: audit_log_workspace (workspace_id, id).
SELECT id, workspace_id, actor_user_id, action, target_type, target_id, request_id, ip_hash, details, at
FROM audit_log
WHERE workspace_id = @workspace_id AND id < @before
ORDER BY id DESC
LIMIT @page_size;

-- name: ListAccountAudit :many
-- Keyset page of a user's own account-level events. Index: audit_log_account.
SELECT id, workspace_id, actor_user_id, action, target_type, target_id, request_id, ip_hash, details, at
FROM audit_log
WHERE workspace_id IS NULL AND actor_user_id = @actor_user_id AND id < @before
ORDER BY id DESC
LIMIT @page_size;
