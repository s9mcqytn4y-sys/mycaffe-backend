-- 20261031000004_harden_payment_idempotency_and_webhook_inbox.down.sql
-- Rollback only after confirming the legacy notification uniqueness key has no collisions.

DROP INDEX IF EXISTS idx_payment_notification_logs_provider_order;
DROP INDEX IF EXISTS uq_payment_notification_logs_event_hash;

ALTER TABLE payment_notification_logs
    DROP CONSTRAINT IF EXISTS chk_payment_notification_event_hash,
    DROP COLUMN IF EXISTS event_hash,
    DROP COLUMN IF EXISTS provider_order_id;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM payment_notification_logs
        GROUP BY provider, order_number, transaction_status, status_code, transaction_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'Cannot restore legacy notification uniqueness: duplicate event keys exist.';
    END IF;
END $$;

ALTER TABLE payment_notification_logs
    ADD CONSTRAINT uq_notif_logs_event
    UNIQUE(provider, order_number, transaction_status, status_code, transaction_id);

DROP INDEX IF EXISTS uq_payments_provider_order_global;

ALTER TABLE payments
    ADD CONSTRAINT uq_payments_provider_order UNIQUE(store_id, provider_order_id);

ALTER TABLE payments
    DROP CONSTRAINT IF EXISTS chk_payments_cash_amounts,
    DROP CONSTRAINT IF EXISTS chk_payments_request_hash,
    DROP COLUMN IF EXISTS change_given_amount,
    DROP COLUMN IF EXISTS cash_tendered_amount,
    DROP COLUMN IF EXISTS request_hash;

-- Intentionally keep PUBLIC EXECUTE revoked on the webhook resolver.
