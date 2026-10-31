-- 20261101000006_email_outbox_and_account_tokens.down.sql

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM audit_logs
        WHERE action IN ('STAFF_INVITED', 'STAFF_PIN_SET', 'STAFF_TEMP_PIN_ISSUED')
    ) THEN
        RAISE EXCEPTION 'Cannot restore audit_logs action constraint: staff lifecycle audit rows exist.';
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
        'EDC_RECONCILE',
        'STAFF_LOGIN_LOCKED',
        'STAFF_SESSION_REVOKED',
        'STAFF_UNLOCK',
        'STAFF_PIN_RESET',
        'STORE_BOOTSTRAP'
    ));

DROP FUNCTION IF EXISTS list_stores_with_due_email();
DROP TABLE IF EXISTS email_outbox;
DROP TABLE IF EXISTS account_tokens;

ALTER TABLE users
    DROP COLUMN IF EXISTS must_change_pin,
    DROP COLUMN IF EXISTS pin_set_at;
