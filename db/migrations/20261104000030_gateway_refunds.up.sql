-- 20261104000030_gateway_refunds.up.sql
-- v0.8 PR 8.5b (RF1-a/b/f): refunds through the Midtrans Direct Refund API. A request lives in
-- gateway_refunds (pending → requested → confirmed | failed); only the bank-confirmed refund enters the
-- append-only payment_refunds ledger and changes payment and order totals.

CREATE TABLE IF NOT EXISTS gateway_refunds (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id           UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    payment_id         UUID NOT NULL,
    order_id           UUID NOT NULL,
    amount             BIGINT NOT NULL,
    lines              JSONB NOT NULL DEFAULT '{}'::jsonb,
    all_lines          BOOLEAN NOT NULL DEFAULT false,
    restore_stock      BOOLEAN NOT NULL,
    reason             TEXT NOT NULL,
    actor_id           UUID NOT NULL,
    approved_by        UUID,
    status             VARCHAR(10) NOT NULL DEFAULT 'pending',
    provider_status    VARCHAR(10) NOT NULL DEFAULT '',
    failure_reason     VARCHAR(500) NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    confirmed_at       TIMESTAMPTZ,
    CONSTRAINT uq_gateway_refunds_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_gateway_refunds_payment FOREIGN KEY (store_id, payment_id) REFERENCES payments(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_gateway_refunds_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_gateway_refunds_actor FOREIGN KEY (store_id, actor_id) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_gateway_refunds_approver FOREIGN KEY (store_id, approved_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_gateway_refunds_amount CHECK (amount > 0),
    CONSTRAINT chk_gateway_refunds_status CHECK (status IN ('pending', 'requested', 'confirmed', 'failed')),
    CONSTRAINT chk_gateway_refunds_confirmed CHECK ((status = 'confirmed') = (confirmed_at IS NOT NULL))
);
-- At most one open request per payment: a second one could refund money twice.
CREATE UNIQUE INDEX IF NOT EXISTS uq_gateway_refunds_open ON gateway_refunds(store_id, payment_id) WHERE status IN ('pending', 'requested');
CREATE INDEX IF NOT EXISTS idx_gateway_refunds_open_age ON gateway_refunds(store_id, updated_at) WHERE status IN ('pending', 'requested');

ALTER TABLE gateway_refunds ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_gateway_refunds_tenant_isolation ON gateway_refunds;
CREATE POLICY p_gateway_refunds_tenant_isolation ON gateway_refunds FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON gateway_refunds TO mycaffe_app;

ALTER TABLE payment_refunds DROP CONSTRAINT IF EXISTS chk_payment_refunds_method;
ALTER TABLE payment_refunds ADD CONSTRAINT chk_payment_refunds_method CHECK (
    (method = 'cash' AND cash_movement_id IS NOT NULL AND bank_name IS NULL AND account_last4 IS NULL AND transfer_reference IS NULL)
    OR (method = 'bank_transfer' AND cash_movement_id IS NULL AND length(trim(bank_name)) > 0
        AND account_last4 ~ '^[0-9]{4}$' AND length(trim(transfer_reference)) > 0)
    OR (method IN ('outlet_cash', 'gateway') AND cash_movement_id IS NULL AND bank_name IS NULL AND account_last4 IS NULL AND transfer_reference IS NULL));

-- RF1-b: partial gateway refunds stay off until proven in the sandbox for the store's acquirer.
ALTER TABLE store_settings ADD COLUMN IF NOT EXISTS gateway_partial_refunds BOOLEAN NOT NULL DEFAULT false;

-- Reconciliation worker discovery, store ids only (same shape as list_stores_with_pending_reconciliation).
CREATE OR REPLACE FUNCTION list_stores_with_open_gateway_refunds(stale_before TIMESTAMPTZ)
RETURNS TABLE (store_id UUID)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT DISTINCT g.store_id FROM gateway_refunds g
    WHERE g.status IN ('pending', 'requested') AND g.updated_at < stale_before
$$;
REVOKE ALL ON FUNCTION list_stores_with_open_gateway_refunds(TIMESTAMPTZ) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION list_stores_with_open_gateway_refunds(TIMESTAMPTZ) TO mycaffe_app;
