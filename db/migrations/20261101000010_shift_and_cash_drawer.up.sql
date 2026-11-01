-- Migration 000010: Shift and Cash Drawer Management (Iteration 4b)
-- Decisions: P6 (1 drawer per terminal, 1 open shift), P7 (cash auto-open, no_sale audit),
-- C1 (blind count, tolerance), H1 (ESC/POS hardware trigger).

-- 1. Cash Drawers Table (Physical / Terminal POS Drawer)
CREATE TABLE IF NOT EXISTS cash_drawers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    name VARCHAR(50) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_cash_drawers_store_name UNIQUE (store_id, name)
);

CREATE INDEX IF NOT EXISTS idx_cash_drawers_store ON cash_drawers(store_id);

-- 2. Shifts Table (Operational Cashier Shift Session)
CREATE TABLE IF NOT EXISTS shifts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    drawer_id UUID NOT NULL REFERENCES cash_drawers(id) ON DELETE RESTRICT,
    opened_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    opening_float BIGINT NOT NULL CHECK (opening_float >= 0),
    opened_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_by UUID REFERENCES users(id) ON DELETE RESTRICT,
    closed_at TIMESTAMPTZ,
    status VARCHAR(20) NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed', 'force_closed')),
    expected_cash BIGINT CHECK (expected_cash IS NULL OR expected_cash >= 0),
    counted_cash BIGINT CHECK (counted_cash IS NULL OR counted_cash >= 0),
    variance BIGINT, -- counted_cash - expected_cash (dapat negatif bila shortage)
    variance_reason TEXT,
    owner_override_by UUID REFERENCES users(id) ON DELETE RESTRICT,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Partial Unique Index: Maksimal 1 shift berstatus 'open' per drawer per toko
CREATE UNIQUE INDEX IF NOT EXISTS idx_shifts_open_drawer ON shifts(store_id, drawer_id) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS idx_shifts_store_opened ON shifts(store_id, opened_at DESC);

-- 3. Cash Movements Table (Append-Only Financial Ledger)
CREATE TABLE IF NOT EXISTS cash_movements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    shift_id UUID NOT NULL REFERENCES shifts(id) ON DELETE RESTRICT,
    movement_type VARCHAR(20) NOT NULL CHECK (
        movement_type IN ('opening_float', 'sale', 'refund', 'paid_out', 'cash_in', 'no_sale', 'tip', 'staff_meal')
    ),
    amount BIGINT NOT NULL CHECK (amount >= 0), -- 0 untuk no_sale
    actor_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    reason TEXT,
    order_id UUID REFERENCES orders(id) ON DELETE RESTRICT,
    payment_id UUID REFERENCES payments(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_cash_movements_shift ON cash_movements(store_id, shift_id, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_cash_movements_payment ON cash_movements(payment_id) WHERE payment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_cash_movements_order ON cash_movements(order_id) WHERE order_id IS NOT NULL;

-- 4. Add shift_id reference on orders table
ALTER TABLE orders ADD COLUMN IF NOT EXISTS shift_id UUID REFERENCES shifts(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_orders_shift_id ON orders(shift_id) WHERE shift_id IS NOT NULL;

-- 5. Append-only anti-tampering trigger on cash_movements
CREATE OR REPLACE FUNCTION prevent_cash_movement_tampering()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'cash_movements table is append-only: updates and deletes are prohibited for financial integrity';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_prevent_cash_movement_tampering ON cash_movements;
CREATE TRIGGER trg_prevent_cash_movement_tampering
BEFORE UPDATE OR DELETE ON cash_movements
FOR EACH ROW EXECUTE FUNCTION prevent_cash_movement_tampering();

-- 6. Row-Level Security (RLS) Policies
ALTER TABLE cash_drawers ENABLE ROW LEVEL SECURITY;
ALTER TABLE shifts ENABLE ROW LEVEL SECURITY;
ALTER TABLE cash_movements ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS p_cash_drawers_tenant_isolation ON cash_drawers;
CREATE POLICY p_cash_drawers_tenant_isolation ON cash_drawers
    FOR ALL
    TO mycaffe_app
    USING (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID)
    WITH CHECK (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID);

DROP POLICY IF EXISTS p_shifts_tenant_isolation ON shifts;
CREATE POLICY p_shifts_tenant_isolation ON shifts
    FOR ALL
    TO mycaffe_app
    USING (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID)
    WITH CHECK (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID);

DROP POLICY IF EXISTS p_cash_movements_tenant_isolation ON cash_movements;
CREATE POLICY p_cash_movements_tenant_isolation ON cash_movements
    FOR ALL
    TO mycaffe_app
    USING (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID)
    WITH CHECK (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID);

-- 7. Grant Privileges to non-owner application runtime role
GRANT SELECT, INSERT, UPDATE, DELETE ON cash_drawers TO mycaffe_app;
GRANT SELECT, INSERT, UPDATE ON shifts TO mycaffe_app;
GRANT SELECT, INSERT ON cash_movements TO mycaffe_app;

-- 8. Extend audit_logs action check constraint
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

