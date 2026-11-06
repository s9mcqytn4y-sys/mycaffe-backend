-- 20261106000039_offline_order_sync.up.sql
-- v0.10 PR 10.2 (ADR-005 OF3/OF6/OF8/OF9, W16): offline orders synced by registered devices. Orders keep
-- their device, client id, offline number and catalog version; every submission is stored as received
-- (accepted or rejected with a reason) so money taken offline is never lost; the owner resolves rejections.

ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS origin VARCHAR(10) NOT NULL DEFAULT 'online',
    ADD COLUMN IF NOT EXISTS client_order_id UUID,
    ADD COLUMN IF NOT EXISTS device_id UUID,
    ADD COLUMN IF NOT EXISTS offline_created_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS offline_queue_number VARCHAR(20),
    ADD COLUMN IF NOT EXISTS catalog_version CHAR(64),
    ADD COLUMN IF NOT EXISTS stale_catalog BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS price_delta BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_origin;
ALTER TABLE orders ADD CONSTRAINT chk_orders_origin CHECK (
    (origin = 'online' AND client_order_id IS NULL AND device_id IS NULL AND offline_created_at IS NULL)
    OR (origin = 'offline' AND client_order_id IS NOT NULL AND device_id IS NOT NULL AND offline_created_at IS NOT NULL
        AND offline_queue_number IS NOT NULL AND catalog_version IS NOT NULL));
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_device;
ALTER TABLE orders ADD CONSTRAINT fk_orders_device FOREIGN KEY (store_id, device_id) REFERENCES devices(store_id, id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX IF NOT EXISTS uq_orders_store_client_order ON orders(store_id, client_order_id) WHERE client_order_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_orders_offline_number ON orders(store_id, offline_queue_number) WHERE offline_queue_number IS NOT NULL;

-- OF8: static QRIS taken offline stays unreconciled until matched with the settlement record (P2-02).
ALTER TABLE payments ADD COLUMN IF NOT EXISTS offline_unreconciled BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS offline_order_submissions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id        UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    device_id       UUID NOT NULL,
    client_order_id UUID NOT NULL,
    payload         JSONB NOT NULL,
    status          VARCHAR(20) NOT NULL,
    reason          VARCHAR(200),
    order_id        UUID,
    resolved_by     UUID,
    resolved_at     TIMESTAMPTZ,
    resolution_note VARCHAR(500),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_offline_submissions_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_offline_submissions_client UNIQUE (store_id, client_order_id),
    CONSTRAINT fk_offline_submissions_device FOREIGN KEY (store_id, device_id) REFERENCES devices(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_offline_submissions_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_offline_submissions_resolver FOREIGN KEY (store_id, resolved_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_offline_submissions_status CHECK (status IN ('accepted', 'rejected', 'resolved_accepted', 'resolved_rejected')),
    CONSTRAINT chk_offline_submissions_order CHECK ((status IN ('accepted', 'resolved_accepted')) = (order_id IS NOT NULL)),
    CONSTRAINT chk_offline_submissions_resolved CHECK ((status LIKE 'resolved_%') = (resolved_at IS NOT NULL AND resolved_by IS NOT NULL)),
    CONSTRAINT chk_offline_submissions_payload CHECK (octet_length(payload::text) <= 65536)
);
CREATE INDEX IF NOT EXISTS idx_offline_submissions_open ON offline_order_submissions(store_id, created_at) WHERE status = 'rejected';

ALTER TABLE offline_order_submissions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_offline_order_submissions_tenant_isolation ON offline_order_submissions;
CREATE POLICY p_offline_order_submissions_tenant_isolation ON offline_order_submissions FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON offline_order_submissions TO mycaffe_app;

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
        'OUTLET_CASH_MOVEMENT',
        'ORDER_DISCOUNT',
        'STAFF_BADGE_ISSUED', 'STAFF_BADGE_REVOKED',
        'TABLE_MOVE', 'TABLE_MERGE', 'TABLE_SPLIT',
        'TABLE_TIMER_EXTEND',
        'DISPLAY_TOKEN_CREATED', 'DISPLAY_TOKEN_REVOKED',
        'PRINTER_CREATED', 'PRINTER_UPDATED', 'PRINTER_TOKEN_ROTATED', 'PRINT_REPRINT',
        'RECEIPT_SENT',
        'DEVICE_REGISTERED', 'DEVICE_RENAMED', 'DEVICE_REVOKED', 'DEVICE_PIN_VERIFIER_ISSUED',
        'OFFLINE_ORDER_SYNCED', 'OFFLINE_ORDER_REJECTED', 'OFFLINE_ORDER_RESOLVED', 'DEVICE_PIN_FAILED'
    )
);
