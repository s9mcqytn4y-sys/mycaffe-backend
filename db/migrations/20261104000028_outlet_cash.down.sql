-- 20261104000028_outlet_cash.down.sql
-- Refuses while outlet cash history exists: the ledger is financial evidence.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM outlet_cash_movements)
       OR EXISTS (SELECT 1 FROM approval_requests WHERE action = 'outlet_expense')
       OR EXISTS (SELECT 1 FROM audit_logs WHERE action = 'OUTLET_CASH_MOVEMENT') THEN
        RAISE EXCEPTION 'Outlet cash history exists; cannot drop outlet_cash_movements.';
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
        'STAFF_PHONE_SET', 'CUSTOMER_PHONE_VERIFIED'
    )
);

ALTER TABLE approval_requests DROP CONSTRAINT IF EXISTS chk_approval_requests_action;
ALTER TABLE approval_requests ADD CONSTRAINT chk_approval_requests_action CHECK (
    action IN ('void', 'refund', 'stock_override', 'shift_variance'));

ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_outlet_cash;
ALTER TABLE store_settings DROP COLUMN IF EXISTS cashier_sees_expected_cash;
ALTER TABLE store_settings DROP COLUMN IF EXISTS default_opening_float;
ALTER TABLE store_settings DROP COLUMN IF EXISTS outlet_daily_expense_limit;

DROP TABLE IF EXISTS outlet_cash_movements;
DROP FUNCTION IF EXISTS prevent_outlet_cash_tampering();
