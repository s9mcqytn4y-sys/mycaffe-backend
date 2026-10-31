-- 20261101000007_store_settings.down.sql

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_logs WHERE action = 'STORE_SETTINGS_CHANGED') THEN
        RAISE EXCEPTION 'Cannot restore audit_logs action constraint: STORE_SETTINGS_CHANGED audit rows exist.';
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
        'STORE_BOOTSTRAP',
        'STAFF_INVITED',
        'STAFF_PIN_SET',
        'STAFF_TEMP_PIN_ISSUED'
    ));

DROP TABLE IF EXISTS store_settings;
