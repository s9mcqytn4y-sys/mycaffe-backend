-- 20261031000004_harden_payment_idempotency_and_webhook_inbox.up.sql
-- Phase 01 Sub-task 04: make payment idempotency and webhook inbox safe for multi-tenant gateway use.

ALTER TABLE payments
    ADD COLUMN IF NOT EXISTS request_hash VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS cash_tendered_amount BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS change_given_amount BIGINT NOT NULL DEFAULT 0;

ALTER TABLE payments
    DROP CONSTRAINT IF EXISTS chk_payments_request_hash,
    DROP CONSTRAINT IF EXISTS chk_payments_cash_amounts;

ALTER TABLE payments
    ADD CONSTRAINT chk_payments_request_hash
        CHECK (request_hash = '' OR length(request_hash) = 64),
    ADD CONSTRAINT chk_payments_cash_amounts
        CHECK (
            cash_tendered_amount >= 0
            AND change_given_amount >= 0
            AND change_given_amount <= cash_tendered_amount
        );

-- Provider order IDs are looked up before tenant context exists. They must therefore
-- be globally unique per provider when a gateway account is shared across stores.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM payments
        GROUP BY provider, provider_order_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'Cannot enforce global provider_order_id uniqueness: duplicate provider/order IDs exist. Reconcile legacy payment attempts before migration.';
    END IF;
END $$;

ALTER TABLE payments
    DROP CONSTRAINT IF EXISTS uq_payments_provider_order;

CREATE UNIQUE INDEX IF NOT EXISTS uq_payments_provider_order_global
    ON payments(provider, provider_order_id);

-- Use the existing durable inbox. Keep legacy rows with empty event_hash out of the
-- new idempotency index; all new webhook rows must provide a SHA-256 event hash.
ALTER TABLE payment_notification_logs
    ADD COLUMN IF NOT EXISTS provider_order_id VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS event_hash VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE payment_notification_logs
    DROP CONSTRAINT IF EXISTS uq_notif_logs_event,
    DROP CONSTRAINT IF EXISTS chk_payment_notification_event_hash;

ALTER TABLE payment_notification_logs
    ADD CONSTRAINT chk_payment_notification_event_hash
        CHECK (event_hash = '' OR length(event_hash) = 64);

CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_notification_logs_event_hash
    ON payment_notification_logs(store_id, event_hash)
    WHERE event_hash <> '';

CREATE INDEX IF NOT EXISTS idx_payment_notification_logs_provider_order
    ON payment_notification_logs(store_id, provider_order_id, created_at DESC)
    WHERE provider_order_id <> '';

-- The resolver is called only after Midtrans signature verification. Remove PostgreSQL's
-- default PUBLIC EXECUTE privilege and keep the explicit application grant.
REVOKE ALL ON FUNCTION resolve_payment_webhook_context(VARCHAR) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_payment_webhook_context(VARCHAR) TO mycaffe_app;
