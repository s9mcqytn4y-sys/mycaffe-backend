-- 20261104000029_partial_refunds.up.sql
-- v0.8 PR 8.5a (RF1-b by item, V13): a payment may be refunded in several parts, each part naming the
-- order lines it returns; the outlet cash fund becomes a third manual refund source. Retried requests
-- are deduplicated by an idempotency key.

ALTER TABLE payment_refunds DROP CONSTRAINT IF EXISTS uq_payment_refunds_payment;
ALTER TABLE payment_refunds ADD COLUMN IF NOT EXISTS idempotency_key VARCHAR(100);
CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_refunds_idempotency ON payment_refunds(store_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_payment_refunds_payment ON payment_refunds(store_id, payment_id);

ALTER TABLE payment_refunds DROP CONSTRAINT IF EXISTS chk_payment_refunds_method;
ALTER TABLE payment_refunds ADD CONSTRAINT chk_payment_refunds_method CHECK (
    (method = 'cash' AND cash_movement_id IS NOT NULL AND bank_name IS NULL AND account_last4 IS NULL AND transfer_reference IS NULL)
    OR (method = 'bank_transfer' AND cash_movement_id IS NULL AND length(trim(bank_name)) > 0
        AND account_last4 ~ '^[0-9]{4}$' AND length(trim(transfer_reference)) > 0)
    OR (method = 'outlet_cash' AND cash_movement_id IS NULL AND bank_name IS NULL AND account_last4 IS NULL AND transfer_reference IS NULL));

CREATE TABLE IF NOT EXISTS payment_refund_lines (
    store_id      UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    refund_id     UUID NOT NULL,
    order_item_id UUID NOT NULL,
    quantity      INT NOT NULL,
    PRIMARY KEY (store_id, refund_id, order_item_id),
    CONSTRAINT fk_payment_refund_lines_refund FOREIGN KEY (store_id, refund_id) REFERENCES payment_refunds(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_payment_refund_lines_item FOREIGN KEY (store_id, order_item_id) REFERENCES order_items(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_payment_refund_lines_quantity CHECK (quantity > 0)
);
CREATE INDEX IF NOT EXISTS idx_payment_refund_lines_item ON payment_refund_lines(store_id, order_item_id);

ALTER TABLE payment_refund_lines ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_payment_refund_lines_tenant_isolation ON payment_refund_lines;
CREATE POLICY p_payment_refund_lines_tenant_isolation ON payment_refund_lines FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT ON payment_refund_lines TO mycaffe_app;
