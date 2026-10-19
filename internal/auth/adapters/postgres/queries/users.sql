-- Every query runs under row-level security as tuorps_app; a user's rows
-- are visible only with app.user_id set to that user. Pre-authentication
-- lookups go through the SECURITY DEFINER functions of migration 00002.

-- name: InsertUser :execrows
INSERT INTO users (id, email, email_normalized, display_name, locale, created_at, updated_at)
VALUES (@id, @email, @email_normalized, @display_name, @locale, @now, @now)
ON CONFLICT (email_normalized) DO NOTHING;

-- name: InsertCredentials :exec
INSERT INTO user_credentials (user_id, password_hash, updated_at) VALUES (@user_id, @password_hash, @now);

-- name: LookupLogin :one
SELECT l.id::uuid AS id, l.email::text AS email, l.display_name::text AS display_name, l.locale::text AS locale,
       l.password_hash::text AS password_hash, l.email_verified::boolean AS email_verified,
       l.totp_enabled::boolean AS totp_enabled, l.disabled::boolean AS disabled
FROM auth_lookup_login(@email_normalized::text)
     AS l(id, email, display_name, locale, password_hash, email_verified, totp_enabled, disabled);

-- name: GetProfile :one
SELECT id, email, display_name, locale, email_verified_at, created_at FROM users WHERE id = @id;

-- name: GetCredentials :one
SELECT password_hash, totp_secret_enc, totp_enabled_at, totp_last_step FROM user_credentials WHERE user_id = @user_id;

-- name: UpdateProfile :execrows
UPDATE users SET display_name = @display_name, locale = @locale, updated_at = @now WHERE id = @id;

-- name: SetPasswordHash :execrows
UPDATE user_credentials SET password_hash = @password_hash, updated_at = @now WHERE user_id = @user_id;

-- name: MarkEmailVerified :execrows
UPDATE users SET email_verified_at = COALESCE(email_verified_at, @now), updated_at = @now WHERE id = @id;

-- name: SupersedeEmailTokens :exec
UPDATE email_tokens SET used_at = @now WHERE user_id = @user_id AND purpose = @purpose AND used_at IS NULL;

-- name: InsertEmailToken :exec
INSERT INTO email_tokens (id, user_id, purpose, token_hash, expires_at, created_at)
VALUES (@id, @user_id, @purpose, @token_hash, @expires_at, @now);

-- name: RedeemEmailToken :one
-- No row when the token is unknown, used, expired or for another purpose.
SELECT r.user_id::uuid AS user_id
FROM (SELECT auth_redeem_email_token(@token_hash, @purpose, @now) AS user_id) r
WHERE r.user_id IS NOT NULL;

-- name: SetPendingTOTP :execrows
UPDATE user_credentials SET totp_secret_enc = @secret_enc, totp_enabled_at = NULL, totp_last_step = NULL, updated_at = @now
WHERE user_id = @user_id;

-- name: EnableTOTP :execrows
UPDATE user_credentials SET totp_enabled_at = @now, totp_last_step = @step::bigint, updated_at = @now
WHERE user_id = @user_id AND totp_secret_enc IS NOT NULL;

-- name: AdvanceTOTPStep :execrows
-- Atomic replay guard: a step is accepted only if it is newer than the last.
UPDATE user_credentials SET totp_last_step = @step::bigint
WHERE user_id = @user_id AND (totp_last_step IS NULL OR totp_last_step < @step::bigint);

-- name: DisableTOTP :execrows
UPDATE user_credentials SET totp_secret_enc = NULL, totp_enabled_at = NULL, totp_last_step = NULL, updated_at = @now
WHERE user_id = @user_id;

-- name: DeleteRecoveryCodes :exec
DELETE FROM recovery_codes WHERE user_id = @user_id;

-- name: InsertRecoveryCode :exec
INSERT INTO recovery_codes (user_id, code_hash) VALUES (@user_id, @code_hash);

-- name: UnusedRecoveryCodes :many
SELECT code_hash FROM recovery_codes WHERE user_id = @user_id AND used_at IS NULL ORDER BY code_hash;

-- name: UseRecoveryCode :execrows
UPDATE recovery_codes SET used_at = @now WHERE user_id = @user_id AND code_hash = @code_hash AND used_at IS NULL;
