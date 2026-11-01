-- Iteration 4c-2: Split Bill and Payment Shift Attribution Migration (Additive)
-- PostgreSQL 18 Compatible
-- Enables shift attribution on payments and indexing for split bill lookups

-- 1. Add shift_id to payments table
ALTER TABLE payments
    ADD COLUMN IF NOT EXISTS shift_id UUID NULL REFERENCES shifts(id) ON DELETE RESTRICT;

-- 2. Indexes for performance and reconciliation queries
CREATE INDEX IF NOT EXISTS idx_payments_store_shift ON payments(store_id, shift_id);
CREATE INDEX IF NOT EXISTS idx_payments_store_split ON payments(store_id, split_id);
CREATE INDEX IF NOT EXISTS idx_payment_splits_order_status ON payment_splits(store_id, order_id, status);

-- 3. Extend audit_logs action check constraint for Tab Close and Reopen lifecycle (Iteration 4c-2)
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
        'TAB_OPEN', 'ROUND_SUBMIT', 'TAB_CLOSE', 'TAB_REOPEN'
    )
);

-- 4. Replace strict unique constraint with partial unique index to allow re-closing after reopen (cancelled splits do not block numbering)
ALTER TABLE payment_splits DROP CONSTRAINT IF EXISTS uq_payment_splits_order_num;
CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_splits_active_order_num
    ON payment_splits(store_id, order_id, split_number)
    WHERE status != 'cancelled';

