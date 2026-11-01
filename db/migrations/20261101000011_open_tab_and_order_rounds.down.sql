-- Migration 000011 Down: Rollback Open Tab, Order Rounds, and Table ID preparation

-- 1. Remove round_id from order_items
ALTER TABLE order_items DROP CONSTRAINT IF EXISTS fk_order_items_round;
DROP INDEX IF EXISTS idx_order_items_round_id;
ALTER TABLE order_items DROP COLUMN IF EXISTS round_id;

-- 2. Drop RLS Policy and order_rounds table
DROP POLICY IF EXISTS p_order_rounds_tenant_isolation ON order_rounds;
DROP TABLE IF EXISTS order_rounds CASCADE;

-- 3. Revert orders status constraint and drop table_id column
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_status;
ALTER TABLE orders ADD CONSTRAINT chk_orders_status CHECK (
    status IN ('pending_payment', 'queued', 'preparing', 'ready', 'served', 'completed', 'cancelled')
);
DROP INDEX IF EXISTS idx_orders_table_id;
ALTER TABLE orders DROP COLUMN IF EXISTS table_id;

-- 4. Revert audit_logs action check constraint to 000010 definition
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
        'CASH_MOVEMENT', 'DRAWER_CREATE', 'DRAWER_UPDATE'
    )
);
