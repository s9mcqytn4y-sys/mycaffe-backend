-- 20261101000017_guest_sessions_and_order_tracking.up.sql
-- Iteration 5b (decisions T2, Q6, QR2, QR5, QR6): one guest session per device bound to a table
-- session, QR pay-first orders bound to it, and a public order tracker behind a hashed random token.

CREATE TABLE IF NOT EXISTS guest_sessions (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id         UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    table_session_id UUID NOT NULL,
    token_hash       BYTEA NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL,
    ended_at         TIMESTAMPTZ,
    CONSTRAINT uq_guest_sessions_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_guest_sessions_token UNIQUE (token_hash),
    CONSTRAINT fk_guest_sessions_table_session FOREIGN KEY (store_id, table_session_id)
        REFERENCES table_sessions(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_guest_sessions_token_hash CHECK (octet_length(token_hash) = 32),
    CONSTRAINT chk_guest_sessions_expiry CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS idx_guest_sessions_table_session ON guest_sessions(store_id, table_session_id);

ALTER TABLE guest_sessions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_guest_sessions_tenant_isolation ON guest_sessions;
CREATE POLICY p_guest_sessions_tenant_isolation ON guest_sessions FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON guest_sessions TO mycaffe_app;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS guest_session_id UUID;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tracking_token_hash BYTEA;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_guest_session;
ALTER TABLE orders ADD CONSTRAINT fk_orders_guest_session
    FOREIGN KEY (store_id, guest_session_id) REFERENCES guest_sessions(store_id, id) ON DELETE RESTRICT;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_tracking_token_hash;
ALTER TABLE orders ADD CONSTRAINT chk_orders_tracking_token_hash
    CHECK (tracking_token_hash IS NULL OR octet_length(tracking_token_hash) = 32);
CREATE INDEX IF NOT EXISTS idx_orders_guest_session ON orders(store_id, guest_session_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_orders_tracking_token_hash
    ON orders(tracking_token_hash) WHERE tracking_token_hash IS NOT NULL;

-- Public tracker (decisions QR2, QR6). The caller hashes the token; only minimal fields leave, and an
-- order stops being visible 24 h after it reached a final state.
CREATE OR REPLACE FUNCTION track_order_by_token(p_token_hash BYTEA)
RETURNS TABLE (
    store_name     VARCHAR,
    order_number   VARCHAR,
    queue_number   VARCHAR,
    status         VARCHAR,
    payment_status VARCHAR,
    updated_at     TIMESTAMPTZ
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT s.name, o.order_number, o.queue_number, o.status, o.payment_status, o.updated_at
    FROM orders o
    JOIN stores s ON s.id = o.store_id AND s.deleted_at IS NULL
    WHERE o.tracking_token_hash = p_token_hash
      AND NOT (o.status IN ('served', 'completed', 'cancelled') AND o.updated_at < NOW() - INTERVAL '24 hours')
    LIMIT 1;
$$;
REVOKE ALL ON FUNCTION track_order_by_token(BYTEA) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION track_order_by_token(BYTEA) TO mycaffe_app;
