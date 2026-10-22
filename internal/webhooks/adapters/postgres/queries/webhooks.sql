-- Webhook receipts and data-deletion requests carry no tenant; the runtime
-- role may only insert them (migration 00003). Reads go through functions.

-- name: InsertReceipt :execrows
INSERT INTO webhook_receipts (provider, dedupe_key, received_at) VALUES (@provider, @dedupe_key, @received_at)
ON CONFLICT DO NOTHING;

-- name: InsertDeletionRequest :exec
INSERT INTO data_deletion_requests (confirmation_code, provider, provider_user_id, status, requested_at)
VALUES (@confirmation_code, @provider, @provider_user_id, 'pending', @requested_at);

-- name: DeletionStatus :one
SELECT COALESCE(data_deletion_status(@confirmation_code::text), '')::text AS status;

-- name: PruneReceipts :one
SELECT webhook_receipts_prune(@before::timestamptz)::bigint AS pruned;
