-- 20261101000016_tables_and_table_sessions.down.sql
-- Refuses while orders reference tables or sessions, or table audit rows exist: history is never dropped.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM orders WHERE table_id IS NOT NULL OR table_session_id IS NOT NULL) THEN
        RAISE EXCEPTION 'Orders reference tables or table sessions; cannot drop them.';
    END IF;
    IF EXISTS (SELECT 1 FROM audit_logs WHERE action IN ('TABLE_CREATE', 'TABLE_UPDATE', 'TABLE_QR_ROTATE', 'TABLE_SESSION_CLOSE')) THEN
        RAISE EXCEPTION 'Table audit rows exist; cannot remove their actions from chk_audit_logs_action.';
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
        'OWNER_PIN_FAILED'
    )
);

DROP FUNCTION IF EXISTS resolve_table_qr(VARCHAR);
DROP INDEX IF EXISTS idx_orders_table_session;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_table_session;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_table;
ALTER TABLE orders DROP COLUMN IF EXISTS table_session_id;
DROP TABLE IF EXISTS table_sessions;
DROP TABLE IF EXISTS tables;
