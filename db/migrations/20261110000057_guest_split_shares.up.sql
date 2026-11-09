-- 20261110000057_guest_split_shares.up.sql
-- ADR-010: per-person payment links for a pay-first QR order. Only the SHA-256 of a link token is stored.

CREATE TABLE IF NOT EXISTS guest_split_shares (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    store_id   UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id   UUID NOT NULL,
    split_id   UUID NOT NULL UNIQUE,
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_guest_split_shares_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_guest_split_shares_split FOREIGN KEY (store_id, split_id) REFERENCES payment_splits(store_id, id) ON DELETE RESTRICT
);

ALTER TABLE guest_split_shares ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_guest_split_shares_tenant_isolation ON guest_split_shares;
CREATE POLICY p_guest_split_shares_tenant_isolation ON guest_split_shares FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT ON guest_split_shares TO mycaffe_app;

-- A link is opened before any tenant context exists; the caller passes the hash, never the token.
CREATE OR REPLACE FUNCTION resolve_split_share(p_hash BYTEA)
RETURNS TABLE (store_id UUID, split_id UUID, order_id UUID, expires_at TIMESTAMPTZ)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT g.store_id, g.split_id, g.order_id, g.expires_at
    FROM guest_split_shares g
    WHERE g.token_hash = p_hash
    LIMIT 1;
$$;
REVOKE ALL ON FUNCTION resolve_split_share(BYTEA) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_split_share(BYTEA) TO mycaffe_app;
