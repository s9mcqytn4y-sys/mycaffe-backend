-- 20261101000005_staff_auth_sessions.up.sql
-- Phase 02 Sub-task 01-B: staff PIN lockout, case-insensitive staff email, persisted
-- auth sessions with rotating refresh tokens, and a narrow pre-auth store resolver (ADR-003).

-- 1. Lockout state on users (ADR-003 D3).
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS failed_pin_attempts INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS chk_users_failed_pin_attempts;

ALTER TABLE users
    ADD CONSTRAINT chk_users_failed_pin_attempts CHECK (failed_pin_attempts >= 0);

-- 2. Login identifier is store_slug + email, so email uniqueness must ignore case.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM users
        GROUP BY store_id, lower(email)
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'Cannot enforce case-insensitive staff email uniqueness: duplicate emails differing only by case exist. Reconcile users before migration.';
    END IF;
END $$;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS uq_users_store_email;

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_store_email_ci
    ON users(store_id, lower(email));

-- 3. Persisted staff sessions. Only SHA-256 hashes of refresh secrets are stored.
--    previous_refresh_token_hash keeps one rotated generation for reuse detection.
CREATE TABLE IF NOT EXISTS auth_sessions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    user_id UUID NOT NULL,
    refresh_token_hash VARCHAR(64) NOT NULL,
    previous_refresh_token_hash VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoked_reason VARCHAR(32),
    user_agent VARCHAR(255) NOT NULL DEFAULT '',
    client_ip VARCHAR(64) NOT NULL DEFAULT '',
    CONSTRAINT uq_auth_sessions_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_auth_sessions_user FOREIGN KEY (store_id, user_id) REFERENCES users(store_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_auth_sessions_refresh_hash CHECK (length(refresh_token_hash) = 64),
    CONSTRAINT chk_auth_sessions_previous_hash CHECK (previous_refresh_token_hash = '' OR length(previous_refresh_token_hash) = 64),
    CONSTRAINT chk_auth_sessions_expiry CHECK (expires_at > created_at),
    CONSTRAINT chk_auth_sessions_revoked CHECK (
        (revoked_at IS NULL AND revoked_reason IS NULL)
        OR (revoked_at IS NOT NULL AND revoked_reason IN (
            'logout', 'refresh_reuse', 'session_limit', 'idle_timeout',
            'user_deactivated', 'pin_reset', 'role_change', 'admin_revoke'
        ))
    )
);

CREATE INDEX IF NOT EXISTS idx_auth_sessions_store_user_active
    ON auth_sessions(store_id, user_id, created_at)
    WHERE revoked_at IS NULL;

ALTER TABLE auth_sessions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS store_isolation_auth_sessions ON auth_sessions;
CREATE POLICY store_isolation_auth_sessions ON auth_sessions FOR ALL
    USING (store_id = current_store_id())
    WITH CHECK (store_id = current_store_id());

-- Sessions are revoked, never deleted, by the runtime role.
GRANT SELECT, INSERT, UPDATE ON auth_sessions TO mycaffe_app;

-- 4. Pre-authentication tenant resolution (ADR-003 D5). Returns only (store_id, is_active)
--    for a non-deleted store; no names, settings, or user data cross the RLS boundary.
CREATE OR REPLACE FUNCTION resolve_store_for_login(p_slug VARCHAR)
RETURNS TABLE (store_id UUID, is_active BOOLEAN)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT s.id, s.is_active
    FROM stores s
    WHERE s.slug = lower(btrim(p_slug))
      AND s.deleted_at IS NULL
    LIMIT 1;
$$;

REVOKE ALL ON FUNCTION resolve_store_for_login(VARCHAR) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_store_for_login(VARCHAR) TO mycaffe_app;

-- 5. Audit actions for the staff auth lifecycle.
ALTER TABLE audit_logs
    DROP CONSTRAINT IF EXISTS chk_audit_logs_action;

ALTER TABLE audit_logs
    ADD CONSTRAINT chk_audit_logs_action
    CHECK (action IN (
        'VOID_ORDER',
        'REFUND',
        'STOCK_OVERRIDE',
        'PRICE_CHANGE',
        'SHIFT_CLOSE',
        'ROLE_CHANGE',
        'STATIC_QRIS_RECONCILE',
        'CASH_PAYMENT_RECORDED',
        'EDC_RECONCILE',
        'STAFF_LOGIN_LOCKED',
        'STAFF_SESSION_REVOKED',
        'STAFF_UNLOCK',
        'STAFF_PIN_RESET',
        'STORE_BOOTSTRAP'
    ));
