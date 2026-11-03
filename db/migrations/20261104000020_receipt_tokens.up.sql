-- 20261104000020_receipt_tokens.up.sql
-- B1 receipts (decision R1): random-token digital receipt links valid 90 days. Only the SHA-256 of
-- the secret is stored; every print issues a new token, older ones stay valid until they expire.

CREATE TABLE IF NOT EXISTS receipt_tokens (
    id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id   UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id   UUID NOT NULL,
    token_hash BYTEA NOT NULL,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT uq_receipt_tokens_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_receipt_tokens_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_receipt_tokens_user FOREIGN KEY (store_id, created_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_receipt_tokens_hash CHECK (octet_length(token_hash) = 32),
    CONSTRAINT chk_receipt_tokens_expiry CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS idx_receipt_tokens_order ON receipt_tokens(store_id, order_id);

ALTER TABLE receipt_tokens ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_receipt_tokens_tenant_isolation ON receipt_tokens;
CREATE POLICY p_receipt_tokens_tenant_isolation ON receipt_tokens FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT ON receipt_tokens TO mycaffe_app;
