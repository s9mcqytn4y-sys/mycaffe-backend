-- 20261104000022_media_uploads.up.sql
-- B3 media (C4, decision K9): presigned uploads to the public media bucket. An upload is pending
-- until /complete re-reads the object, verifies it and writes the resized variants; only then may
-- it become products.image_url.

CREATE TABLE IF NOT EXISTS media_uploads (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id     UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    kind         VARCHAR(30) NOT NULL,
    product_id   UUID NOT NULL,
    status       VARCHAR(20) NOT NULL DEFAULT 'pending',
    object_key   TEXT NOT NULL,
    content_type VARCHAR(50) NOT NULL,
    size_bytes   BIGINT NOT NULL,
    variants     JSONB,
    created_by   UUID NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CONSTRAINT uq_media_uploads_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_media_uploads_product FOREIGN KEY (store_id, product_id) REFERENCES products(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_media_uploads_user FOREIGN KEY (store_id, created_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_media_uploads_kind CHECK (kind IN ('product_image')),
    CONSTRAINT chk_media_uploads_status CHECK (status IN ('pending', 'completed')),
    CONSTRAINT chk_media_uploads_type CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp')),
    CONSTRAINT chk_media_uploads_size CHECK (size_bytes > 0 AND size_bytes <= 5242880),
    CONSTRAINT chk_media_uploads_completed CHECK ((status = 'completed') = (completed_at IS NOT NULL AND variants IS NOT NULL)),
    CONSTRAINT chk_media_uploads_expiry CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS idx_media_uploads_product ON media_uploads(store_id, product_id);

ALTER TABLE media_uploads ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_media_uploads_tenant_isolation ON media_uploads;
CREATE POLICY p_media_uploads_tenant_isolation ON media_uploads FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON media_uploads TO mycaffe_app;

-- PRODUCT_IMAGE_CHANGED records which upload replaced a product photo.
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
        'PRODUCT_IMAGE_CHANGED'
    )
);
