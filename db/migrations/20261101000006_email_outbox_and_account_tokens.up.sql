-- 20261101000006_email_outbox_and_account_tokens.up.sql
-- Owner decisions E1/E2/E5: 6-digit PIN, staff invitation + PIN reset links, and a durable
-- PostgreSQL email outbox delivered by an in-process worker (no Redis, ADR-002).

-- 1. Account lifecycle flags on users.
--    pin_set_at IS NULL     -> invited, has not chosen a PIN yet (cannot log in)
--    must_change_pin = true -> logged in with an owner-issued temporary PIN; must change it
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS pin_set_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS must_change_pin BOOLEAN NOT NULL DEFAULT false;

UPDATE users SET pin_set_at = created_at WHERE pin_set_at IS NULL AND pin_hash <> '';

-- 2. Single-use account tokens (activation / PIN reset). Only SHA-256 of the secret is stored.
CREATE TABLE IF NOT EXISTS account_tokens (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    user_id UUID NOT NULL,
    purpose VARCHAR(20) NOT NULL,
    token_hash VARCHAR(64) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_account_tokens_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_account_tokens_hash UNIQUE (token_hash),
    CONSTRAINT fk_account_tokens_user FOREIGN KEY (store_id, user_id) REFERENCES users(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_account_tokens_creator FOREIGN KEY (store_id, created_by) REFERENCES users(store_id, id) ON DELETE SET NULL (created_by),
    CONSTRAINT chk_account_tokens_purpose CHECK (purpose IN ('activation', 'pin_reset')),
    CONSTRAINT chk_account_tokens_hash CHECK (length(token_hash) = 64),
    CONSTRAINT chk_account_tokens_expiry CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS idx_account_tokens_user_open
    ON account_tokens(store_id, user_id, purpose)
    WHERE used_at IS NULL;

ALTER TABLE account_tokens ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS store_isolation_account_tokens ON account_tokens;
CREATE POLICY store_isolation_account_tokens ON account_tokens FOR ALL
    USING (store_id = current_store_id())
    WITH CHECK (store_id = current_store_id());

GRANT SELECT, INSERT, UPDATE ON account_tokens TO mycaffe_app;

-- 3. Durable email outbox. Rows are written in the same transaction as the business event.
--    Bodies are scrubbed after delivery because activation/reset emails contain link secrets.
CREATE TABLE IF NOT EXISTS email_outbox (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    template VARCHAR(50) NOT NULL,
    to_email VARCHAR(255) NOT NULL,
    to_name VARCHAR(255) NOT NULL DEFAULT '',
    subject VARCHAR(255) NOT NULL,
    html_body TEXT NOT NULL,
    text_body TEXT NOT NULL,
    reply_to VARCHAR(255) NOT NULL DEFAULT '',
    reference_id VARCHAR(100) NOT NULL DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_until TIMESTAMPTZ,
    last_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at TIMESTAMPTZ,
    CONSTRAINT uq_email_outbox_store_id UNIQUE (store_id, id),
    CONSTRAINT chk_email_outbox_status CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    CONSTRAINT chk_email_outbox_attempts CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_email_outbox_due
    ON email_outbox(store_id, next_attempt_at)
    WHERE status IN ('pending', 'sending');

ALTER TABLE email_outbox ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS store_isolation_email_outbox ON email_outbox;
CREATE POLICY store_isolation_email_outbox ON email_outbox FOR ALL
    USING (store_id = current_store_id())
    WITH CHECK (store_id = current_store_id());

GRANT SELECT, INSERT, UPDATE ON email_outbox TO mycaffe_app;

-- 4. Worker tenant discovery (same pattern as list_stores_with_pending_reconciliation):
--    returns only store UUIDs that have deliverable email; rows are then read under RLS.
CREATE OR REPLACE FUNCTION list_stores_with_due_email()
RETURNS TABLE (store_id UUID)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT DISTINCT e.store_id
    FROM email_outbox e
    WHERE (e.status = 'pending' AND e.next_attempt_at <= NOW())
       OR (e.status = 'sending' AND e.locked_until IS NOT NULL AND e.locked_until <= NOW())
$$;

REVOKE ALL ON FUNCTION list_stores_with_due_email() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION list_stores_with_due_email() TO mycaffe_app;

-- 5. Audit actions for staff lifecycle.
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
        'STORE_BOOTSTRAP',
        'STAFF_INVITED',
        'STAFF_PIN_SET',
        'STAFF_TEMP_PIN_ISSUED'
    ));
