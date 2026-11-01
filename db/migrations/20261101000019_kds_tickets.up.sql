-- 20261101000019_kds_tickets.up.sql
-- Iteration 5d (decisions P2, P4, Q7, C3): one KDS ticket per (order or round) x station. Order lines
-- snapshot their prep station so later catalog edits never move a ticket.

ALTER TABLE order_items ADD COLUMN IF NOT EXISTS snapshot_prep_station VARCHAR(10) NOT NULL DEFAULT 'none';
UPDATE order_items oi SET snapshot_prep_station = p.prep_station
FROM products p
WHERE p.store_id = oi.store_id AND p.id = oi.product_id AND oi.snapshot_prep_station <> p.prep_station;
ALTER TABLE order_items DROP CONSTRAINT IF EXISTS chk_order_items_prep_station;
ALTER TABLE order_items ADD CONSTRAINT chk_order_items_prep_station
    CHECK (snapshot_prep_station IN ('bar', 'kitchen', 'none'));

CREATE TABLE IF NOT EXISTS kds_tickets (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id           UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id           UUID NOT NULL,
    round_id           UUID,
    station            VARCHAR(10) NOT NULL,
    status             VARCHAR(12) NOT NULL DEFAULT 'queued',
    late_payment       BOOLEAN NOT NULL DEFAULT false,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at         TIMESTAMPTZ,
    ready_at           TIMESTAMPTZ,
    served_at          TIMESTAMPTZ,
    cancelled_at       TIMESTAMPTZ,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by_user_id UUID,
    CONSTRAINT uq_kds_tickets_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_kds_tickets_unit UNIQUE NULLS NOT DISTINCT (store_id, order_id, round_id, station),
    CONSTRAINT fk_kds_tickets_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_kds_tickets_round FOREIGN KEY (store_id, round_id) REFERENCES order_rounds(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_kds_tickets_updated_by FOREIGN KEY (store_id, updated_by_user_id)
        REFERENCES users(store_id, id) ON DELETE SET NULL (updated_by_user_id),
    CONSTRAINT chk_kds_tickets_station CHECK (station IN ('bar', 'kitchen')),
    CONSTRAINT chk_kds_tickets_status CHECK (status IN ('queued', 'preparing', 'ready', 'served', 'cancelled'))
);
CREATE INDEX IF NOT EXISTS idx_kds_tickets_active ON kds_tickets(store_id, station, status, created_at)
    WHERE status IN ('queued', 'preparing', 'ready');

ALTER TABLE kds_tickets ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_kds_tickets_tenant_isolation ON kds_tickets;
CREATE POLICY p_kds_tickets_tenant_isolation ON kds_tickets FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON kds_tickets TO mycaffe_app;
