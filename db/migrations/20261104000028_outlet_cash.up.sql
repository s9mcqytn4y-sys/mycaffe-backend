-- 20261104000028_outlet_cash.up.sql
-- v0.8 PR 8.4 (decision V13): an outlet cash fund separate from the cashier drawers. Append-only ledger;
-- the balance is the signed sum of its movements. Cashier settings: default opening float and whether
-- a cashier may see the expected drawer cash before closing (blind count stays the default).

CREATE TABLE IF NOT EXISTS outlet_cash_movements (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id         UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    movement_type    VARCHAR(12) NOT NULL,
    amount           BIGINT NOT NULL,
    category         VARCHAR(20),
    note             TEXT NOT NULL,
    shift_id         UUID,
    cash_movement_id UUID,
    payment_id       UUID,
    approval_id      UUID,
    actor_id         UUID NOT NULL,
    approved_by      UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_outlet_cash_movements_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_outlet_cash_actor FOREIGN KEY (store_id, actor_id) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_outlet_cash_approver FOREIGN KEY (store_id, approved_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_outlet_cash_movement FOREIGN KEY (store_id, cash_movement_id) REFERENCES cash_movements(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_outlet_cash_payment FOREIGN KEY (store_id, payment_id) REFERENCES payments(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_outlet_cash_approval FOREIGN KEY (store_id, approval_id) REFERENCES approval_requests(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_outlet_cash_type CHECK (movement_type IN ('top_up', 'expense', 'refund', 'to_drawer', 'from_drawer')),
    CONSTRAINT chk_outlet_cash_amount CHECK (amount > 0),
    CONSTRAINT chk_outlet_cash_note CHECK (length(btrim(note)) > 0),
    CONSTRAINT chk_outlet_cash_category CHECK (
        (movement_type = 'expense' AND category IN ('ingredients', 'operational', 'other'))
        OR (movement_type <> 'expense' AND category IS NULL)),
    CONSTRAINT chk_outlet_cash_drawer_link CHECK (
        (movement_type IN ('to_drawer', 'from_drawer')) = (cash_movement_id IS NOT NULL AND shift_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_outlet_cash_store_created ON outlet_cash_movements(store_id, created_at);

CREATE OR REPLACE FUNCTION prevent_outlet_cash_tampering() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'outlet_cash_movements is append-only: updates and deletes are prohibited for financial integrity';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_prevent_outlet_cash_tampering ON outlet_cash_movements;
CREATE TRIGGER trg_prevent_outlet_cash_tampering BEFORE UPDATE OR DELETE ON outlet_cash_movements
    FOR EACH ROW EXECUTE FUNCTION prevent_outlet_cash_tampering();

ALTER TABLE outlet_cash_movements ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_outlet_cash_movements_tenant_isolation ON outlet_cash_movements;
CREATE POLICY p_outlet_cash_movements_tenant_isolation ON outlet_cash_movements FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT ON outlet_cash_movements TO mycaffe_app;

ALTER TABLE store_settings ADD COLUMN IF NOT EXISTS outlet_daily_expense_limit BIGINT NOT NULL DEFAULT 0;
ALTER TABLE store_settings ADD COLUMN IF NOT EXISTS default_opening_float BIGINT NOT NULL DEFAULT 0;
ALTER TABLE store_settings ADD COLUMN IF NOT EXISTS cashier_sees_expected_cash BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_outlet_cash;
ALTER TABLE store_settings ADD CONSTRAINT chk_store_settings_outlet_cash CHECK (
    outlet_daily_expense_limit BETWEEN 0 AND 100000000 AND default_opening_float BETWEEN 0 AND 100000000);

ALTER TABLE approval_requests DROP CONSTRAINT IF EXISTS chk_approval_requests_action;
ALTER TABLE approval_requests ADD CONSTRAINT chk_approval_requests_action CHECK (
    action IN ('void', 'refund', 'stock_override', 'shift_variance', 'outlet_expense'));

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
        'OUTLET_CASH_MOVEMENT'
    )
);
