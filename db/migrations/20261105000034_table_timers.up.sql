-- 20261105000034_table_timers.up.sql
-- v0.9 PR 9.2 (decision W6): table time limit, warnings only. A session opened while the store has
-- a limit gets ends_at; a worker marks warned_at and expired_at exactly once and publishes events.
-- Orders are never refused because of the timer. Extending moves ends_at and is audited.

ALTER TABLE store_settings
    ADD COLUMN IF NOT EXISTS table_time_limit_minutes INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS table_timer_warning_minutes INT NOT NULL DEFAULT 10;
ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_table_time_limit;
ALTER TABLE store_settings ADD CONSTRAINT chk_store_settings_table_time_limit
    CHECK (table_time_limit_minutes = 0 OR table_time_limit_minutes BETWEEN 15 AND 480);
ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_table_timer_warning;
ALTER TABLE store_settings ADD CONSTRAINT chk_store_settings_table_timer_warning
    CHECK (table_timer_warning_minutes BETWEEN 1 AND 60);

ALTER TABLE table_sessions
    ADD COLUMN IF NOT EXISTS ends_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS warned_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS expired_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS extended_minutes INT NOT NULL DEFAULT 0;
ALTER TABLE table_sessions DROP CONSTRAINT IF EXISTS chk_table_sessions_timer;
ALTER TABLE table_sessions ADD CONSTRAINT chk_table_sessions_timer CHECK (
    extended_minutes >= 0
    AND (ends_at IS NOT NULL OR (warned_at IS NULL AND expired_at IS NULL AND extended_minutes = 0))
);
CREATE INDEX IF NOT EXISTS idx_table_sessions_timer_due
    ON table_sessions(store_id, ends_at) WHERE status = 'open' AND ends_at IS NOT NULL AND expired_at IS NULL;

-- Worker tenant discovery, same shape as list_stores_with_due_message: store ids only.
CREATE OR REPLACE FUNCTION list_stores_with_due_table_timers()
RETURNS TABLE (store_id UUID)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT DISTINCT ts.store_id
    FROM table_sessions ts
    LEFT JOIN store_settings ss ON ss.store_id = ts.store_id
    WHERE ts.status = 'open' AND ts.ends_at IS NOT NULL AND ts.expired_at IS NULL
      AND (ts.ends_at <= NOW()
           OR (ts.warned_at IS NULL
               AND ts.ends_at - make_interval(mins => COALESCE(ss.table_timer_warning_minutes, 10)) <= NOW()))
$$;
REVOKE ALL ON FUNCTION list_stores_with_due_table_timers() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION list_stores_with_due_table_timers() TO mycaffe_app;

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
        'TABLE_MOVE', 'TABLE_MERGE', 'TABLE_SPLIT',
        'TABLE_TIMER_EXTEND'
    )
);
