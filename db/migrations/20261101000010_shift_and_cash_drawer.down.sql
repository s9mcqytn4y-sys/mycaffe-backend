-- Migration 000010 Down: Rollback Shift and Cash Drawer Management

-- 1. Remove shift_id column from orders
DROP INDEX IF EXISTS idx_orders_shift_id;
ALTER TABLE orders DROP COLUMN IF EXISTS shift_id;

-- 2. Drop anti-tampering trigger & function
DROP TRIGGER IF EXISTS trg_prevent_cash_movement_tampering ON cash_movements;
DROP FUNCTION IF EXISTS prevent_cash_movement_tampering();

-- 3. Drop RLS Policies
DROP POLICY IF EXISTS p_cash_movements_tenant_isolation ON cash_movements;
DROP POLICY IF EXISTS p_shifts_tenant_isolation ON shifts;
DROP POLICY IF EXISTS p_cash_drawers_tenant_isolation ON cash_drawers;

-- 4. Drop Tables
DROP TABLE IF EXISTS cash_movements CASCADE;
DROP TABLE IF EXISTS shifts CASCADE;
DROP TABLE IF EXISTS cash_drawers CASCADE;

-- 5. Revert audit_logs action check constraint to 000005 definition
ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS chk_audit_logs_action;
ALTER TABLE audit_logs ADD CONSTRAINT chk_audit_logs_action CHECK (
    action IN (
        'VOID_ORDER', 'REFUND', 'STOCK_OVERRIDE', 'PRICE_CHANGE', 'SHIFT_CLOSE',
        'ROLE_CHANGE', 'STATIC_QRIS_RECONCILE', 'ORDER_CREATE', 'ORDER_CANCEL',
        'PAYMENT_SETTLED', 'STAFF_LOGIN_LOCKED', 'STAFF_UNLOCK', 'STAFF_PIN_RESET',
        'STAFF_DEACTIVATED', 'STAFF_SESSION_REVOKED'
    )
);

