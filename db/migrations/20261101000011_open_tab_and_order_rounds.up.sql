-- Migration 000011: Open Tab, Order Rounds, and Table ID preparation (Iteration 4c, PR 4c-1)
-- Decisions: P2 (One tab = one order with rounds, frozen prices snapshot, outbox event order.round_submitted),
-- T2 (Cashier/Owner manages tabs), and table_id preparation for Iteration 5.

-- 1. Extend orders table:
-- Add table_id column (nullable UUID, to be referenced by tables in Iteration 5)
ALTER TABLE orders ADD COLUMN IF NOT EXISTS table_id UUID DEFAULT NULL;
CREATE INDEX IF NOT EXISTS idx_orders_table_id ON orders(store_id, table_id);

-- Update chk_orders_status to allow 'open' status for tabs
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_status;
ALTER TABLE orders ADD CONSTRAINT chk_orders_status CHECK (status IN ('pending_payment', 'open', 'queued', 'preparing', 'ready', 'served', 'completed', 'cancelled'));

-- 2. Order Rounds Table
CREATE TABLE IF NOT EXISTS order_rounds (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id UUID NOT NULL,
    round_number INT NOT NULL,
    status VARCHAR(30) NOT NULL DEFAULT 'queued',
    submitted_by_user_id UUID,
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    notes TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_order_rounds_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_order_rounds_order_round UNIQUE (store_id, order_id, round_number),
    CONSTRAINT fk_order_rounds_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_order_rounds_user FOREIGN KEY (store_id, submitted_by_user_id) REFERENCES users(store_id, id) ON DELETE SET NULL,
    CONSTRAINT chk_order_rounds_round_number CHECK (round_number > 0),
    CONSTRAINT chk_order_rounds_status CHECK (status IN ('queued', 'preparing', 'ready', 'served', 'cancelled'))
);

CREATE INDEX IF NOT EXISTS idx_order_rounds_order ON order_rounds(store_id, order_id, round_number);

-- 3. Link order_items to order_rounds
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS round_id UUID DEFAULT NULL;
ALTER TABLE order_items DROP CONSTRAINT IF EXISTS fk_order_items_round;
ALTER TABLE order_items ADD CONSTRAINT fk_order_items_round
    FOREIGN KEY (store_id, round_id) REFERENCES order_rounds(store_id, id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_order_items_round_id ON order_items(store_id, round_id);

-- 4. Row-Level Security (RLS) on order_rounds
ALTER TABLE order_rounds ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS p_order_rounds_tenant_isolation ON order_rounds;
CREATE POLICY p_order_rounds_tenant_isolation ON order_rounds
    FOR ALL
    TO mycaffe_app
    USING (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID)
    WITH CHECK (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID);

-- 5. Grant permissions to non-owner application runtime role
GRANT SELECT, INSERT, UPDATE ON order_rounds TO mycaffe_app;

-- 6. Extend audit_logs action check constraint
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
