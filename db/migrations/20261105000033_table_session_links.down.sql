-- 20261105000033_table_session_links.down.sql
-- Refuses while merge history exists.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM table_session_links)
       OR EXISTS (SELECT 1 FROM table_sessions WHERE close_reason = 'merged')
       OR EXISTS (SELECT 1 FROM guest_sessions WHERE table_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM audit_logs WHERE action IN ('TABLE_MOVE', 'TABLE_MERGE', 'TABLE_SPLIT')) THEN
        RAISE EXCEPTION 'Table move/merge history or scanned-table guest sessions exist; cannot roll back 000033.';
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
        'STAFF_BADGE_ISSUED', 'STAFF_BADGE_REVOKED'
    )
);

ALTER TABLE table_sessions DROP CONSTRAINT IF EXISTS chk_table_sessions_reason;
ALTER TABLE table_sessions ADD CONSTRAINT chk_table_sessions_reason
    CHECK (close_reason IS NULL OR close_reason IN ('cashier', 'auto'));

ALTER TABLE guest_sessions DROP CONSTRAINT IF EXISTS fk_guest_sessions_table;
ALTER TABLE guest_sessions DROP COLUMN IF EXISTS table_id;

DROP TABLE IF EXISTS table_session_links;
