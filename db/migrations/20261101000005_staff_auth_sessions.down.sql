-- 20261101000005_staff_auth_sessions.down.sql
-- Rollback of staff auth sessions. Refuses to run if audit rows use the new actions,
-- because the restored CHECK constraint would reject them.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM audit_logs
        WHERE action IN ('STAFF_LOGIN_LOCKED', 'STAFF_SESSION_REVOKED', 'STAFF_UNLOCK', 'STAFF_PIN_RESET', 'STORE_BOOTSTRAP')
    ) THEN
        RAISE EXCEPTION 'Cannot restore audit_logs action constraint: staff auth audit rows exist.';
    END IF;
END $$;

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
        'EDC_RECONCILE'
    ));

DROP FUNCTION IF EXISTS resolve_store_for_login(VARCHAR);

DROP TABLE IF EXISTS auth_sessions;

DROP INDEX IF EXISTS uq_users_store_email_ci;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS uq_users_store_email;

ALTER TABLE users
    ADD CONSTRAINT uq_users_store_email UNIQUE (store_id, email);

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS chk_users_failed_pin_attempts;

ALTER TABLE users
    DROP COLUMN IF EXISTS locked_until,
    DROP COLUMN IF EXISTS failed_pin_attempts;
