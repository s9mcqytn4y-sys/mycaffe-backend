-- Rollback: 20261101000012_split_bill_and_payment_shift.down.sql

DROP INDEX IF EXISTS uq_payment_splits_active_order_num;
ALTER TABLE payment_splits DROP CONSTRAINT IF EXISTS uq_payment_splits_order_num;
ALTER TABLE payment_splits ADD CONSTRAINT uq_payment_splits_order_num UNIQUE(store_id, order_id, split_number);

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
        'TAB_OPEN', 'ROUND_SUBMIT'
    )
);

DROP INDEX IF EXISTS idx_payment_splits_order_status;
DROP INDEX IF EXISTS idx_payments_store_split;
DROP INDEX IF EXISTS idx_payments_store_shift;

ALTER TABLE payments
    DROP COLUMN IF EXISTS shift_id;

