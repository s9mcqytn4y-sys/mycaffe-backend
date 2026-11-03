-- 20261104000023_payment_refunds.up.sql
-- B4 / RF1-c, RF1-e: append-only refund ledger (cash from the drawer or an owner's manual bank
-- transfer, for any original payment method) and per-role refund windows in store settings.

-- Composite keys so the ledger can reference payments and cash movements within the same store.
ALTER TABLE payments DROP CONSTRAINT IF EXISTS uq_payments_store_id;
ALTER TABLE payments ADD CONSTRAINT uq_payments_store_id UNIQUE (store_id, id);
ALTER TABLE cash_movements DROP CONSTRAINT IF EXISTS uq_cash_movements_store_id;
ALTER TABLE cash_movements ADD CONSTRAINT uq_cash_movements_store_id UNIQUE (store_id, id);

CREATE TABLE IF NOT EXISTS payment_refunds (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id           UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    payment_id         UUID NOT NULL,
    order_id           UUID NOT NULL,
    amount             BIGINT NOT NULL,
    method             VARCHAR(20) NOT NULL,
    bank_name          VARCHAR(100),
    account_last4      CHAR(4),
    transfer_reference VARCHAR(100),
    cash_movement_id   UUID,
    reason             TEXT NOT NULL,
    actor_id           UUID NOT NULL,
    approved_by        UUID,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_payment_refunds_store_id UNIQUE (store_id, id),
    -- Full refunds only (RF1-b partial refunds come later): one refund per payment, retries included.
    CONSTRAINT uq_payment_refunds_payment UNIQUE (store_id, payment_id),
    CONSTRAINT fk_payment_refunds_payment FOREIGN KEY (store_id, payment_id) REFERENCES payments(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_payment_refunds_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_payment_refunds_movement FOREIGN KEY (store_id, cash_movement_id) REFERENCES cash_movements(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_payment_refunds_actor FOREIGN KEY (store_id, actor_id) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_payment_refunds_approver FOREIGN KEY (store_id, approved_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_payment_refunds_amount CHECK (amount > 0),
    CONSTRAINT chk_payment_refunds_method CHECK (
        (method = 'cash' AND cash_movement_id IS NOT NULL AND bank_name IS NULL AND account_last4 IS NULL AND transfer_reference IS NULL)
        OR (method = 'bank_transfer' AND cash_movement_id IS NULL AND length(trim(bank_name)) > 0
            AND account_last4 ~ '^[0-9]{4}$' AND length(trim(transfer_reference)) > 0))
);
CREATE INDEX IF NOT EXISTS idx_payment_refunds_order ON payment_refunds(store_id, order_id);

ALTER TABLE payment_refunds ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_payment_refunds_tenant_isolation ON payment_refunds;
CREATE POLICY p_payment_refunds_tenant_isolation ON payment_refunds FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT ON payment_refunds TO mycaffe_app;

-- RF1-e: cashier same business day (0), owner up to 7 days; older payments cannot be refunded in the system.
ALTER TABLE store_settings ADD COLUMN IF NOT EXISTS refund_window_cashier_days INT NOT NULL DEFAULT 0;
ALTER TABLE store_settings ADD COLUMN IF NOT EXISTS refund_window_owner_days INT NOT NULL DEFAULT 7;
ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_refund_window;
ALTER TABLE store_settings ADD CONSTRAINT chk_store_settings_refund_window CHECK (
    refund_window_cashier_days BETWEEN 0 AND 30 AND refund_window_owner_days BETWEEN 0 AND 30
    AND refund_window_owner_days >= refund_window_cashier_days);
