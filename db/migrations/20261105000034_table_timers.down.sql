-- 20261105000034_table_timers.down.sql
-- Refuses while timer history exists.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM table_sessions WHERE ends_at IS NOT NULL)
       OR EXISTS (SELECT 1 FROM store_settings WHERE table_time_limit_minutes <> 0)
       OR EXISTS (SELECT 1 FROM audit_logs WHERE action = 'TABLE_TIMER_EXTEND') THEN
        RAISE EXCEPTION 'Table timer history exists; cannot roll back 000034.';
    END IF;
END $$;

ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS chk_audit_logs_action;
ALTER TABLE audit_logs ADD CONSTRAINT chk_audit_logs_action CHECK (
    action IN (
        'VOID_ORDER', 'REFUND', 'STOCK_OVERRIDE', 'PRICE_CHANGE',
        'SHIFT_OPEN', 'SHIFT_CLOSE', 'SHIFT_FORCE_CLOSE',
        'ROLE_CHANGE', 'STATIC_QRIS_RECONCILE', 'CASH_PAYMENT_RECORDED', 'EDC_RECONCILE',
        'ORDER_CREATE', 'ORDER_CANCEL', 'PAYMENT_SETTLED',
        'STAFF_LOGIN_LOCKED', 'STAFF_UNLOCK', 'STAFF_PIN_RESET', 'STAFF_DEACTIVATED',
        'STAFF_SESSION_REVOKED', 'STAFF_INVITED', 'STAFF_PIN_SET', 'STAFF_TEMP_PIN_ISSUED',
        'STORE_BOOTSTRAP', 'STORE_SETTINGS_CHANGED',
        'CASH_MOVEMENT', 'DRAWER_CREATE', 'DRAWER_UPDATE',
        'TAB_OPEN', 'ROUND_SUBMIT', 'TAB_CLOSE', 'TAB_REOPEN',
        'OWNER_PIN_FAILED',
        'TABLE_CREATE', 'TABLE_UPDATE', 'TABLE_QR_ROTATE', 'TABLE_SESSION_CLOSE',
        'PRODUCT_IMAGE_CHANGED',
        'APPROVAL_REQUESTED', 'APPROVAL_APPROVED', 'APPROVAL_REJECTED',
        'STAFF_PHONE_SET', 'CUSTOMER_PHONE_VERIFIED',
        'OUTLET_CASH_MOVEMENT',
        'ORDER_DISCOUNT',
        'STAFF_BADGE_ISSUED', 'STAFF_BADGE_REVOKED',
        'TABLE_MOVE', 'TABLE_MERGE', 'TABLE_SPLIT'
    )
);

DROP FUNCTION IF EXISTS list_stores_with_due_table_timers();
DROP INDEX IF EXISTS idx_table_sessions_timer_due;
ALTER TABLE table_sessions DROP CONSTRAINT IF EXISTS chk_table_sessions_timer;
ALTER TABLE table_sessions
    DROP COLUMN IF EXISTS ends_at,
    DROP COLUMN IF EXISTS warned_at,
    DROP COLUMN IF EXISTS expired_at,
    DROP COLUMN IF EXISTS extended_minutes;
ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_table_time_limit;
ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_table_timer_warning;
ALTER TABLE store_settings
    DROP COLUMN IF EXISTS table_time_limit_minutes,
    DROP COLUMN IF EXISTS table_timer_warning_minutes;
