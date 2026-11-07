-- 20261107000044_bundles.up.sql
-- v0.11 PR 11.2 (K7, decision X4): fixed-price bundles. A bundle is a product of type 'bundle' whose default
-- variant carries the bundle price; it has slots ("Kopi", "Pastry"), each with a quantity and the products the
-- customer may choose from. When sold, every chosen component becomes its own order line, so stock, recipes,
-- KDS stations, printers, line refunds and per-menu reports keep working; the bundle price is allocated to
-- the components in proportion to their normal prices and the lines share a bundle_group_id.

ALTER TABLE products DROP CONSTRAINT IF EXISTS chk_products_type;
ALTER TABLE products ADD CONSTRAINT chk_products_type CHECK (product_type IN ('prepared', 'ready_to_serve', 'bundle'));
ALTER TABLE products DROP CONSTRAINT IF EXISTS chk_products_bundle;
ALTER TABLE products ADD CONSTRAINT chk_products_bundle CHECK (product_type <> 'bundle' OR (NOT track_stock AND prep_station = 'none'));

CREATE TABLE IF NOT EXISTS bundle_slots (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id          UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    bundle_product_id UUID NOT NULL,
    name              VARCHAR(50) NOT NULL,
    quantity          INT NOT NULL DEFAULT 1,
    display_order     INT NOT NULL DEFAULT 0,
    product_ids       JSONB NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_bundle_slots_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_bundle_slots_bundle FOREIGN KEY (store_id, bundle_product_id) REFERENCES products(store_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_bundle_slots_name CHECK (length(btrim(name)) > 0),
    CONSTRAINT chk_bundle_slots_quantity CHECK (quantity BETWEEN 1 AND 10),
    CONSTRAINT chk_bundle_slots_choices CHECK (jsonb_typeof(product_ids) = 'array' AND jsonb_array_length(product_ids) BETWEEN 1 AND 50)
);
CREATE INDEX IF NOT EXISTS idx_bundle_slots_bundle ON bundle_slots(store_id, bundle_product_id, display_order);

ALTER TABLE bundle_slots ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_bundle_slots_tenant_isolation ON bundle_slots;
CREATE POLICY p_bundle_slots_tenant_isolation ON bundle_slots FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
-- Slots are replaced as a whole when the owner edits a bundle.
GRANT SELECT, INSERT, UPDATE, DELETE ON bundle_slots TO mycaffe_app;

ALTER TABLE order_items
    ADD COLUMN IF NOT EXISTS bundle_product_id UUID,
    ADD COLUMN IF NOT EXISTS bundle_group_id UUID,
    ADD COLUMN IF NOT EXISTS snapshot_bundle_name VARCHAR(150);
ALTER TABLE order_items DROP CONSTRAINT IF EXISTS chk_order_items_bundle;
ALTER TABLE order_items ADD CONSTRAINT chk_order_items_bundle CHECK (
    (bundle_product_id IS NULL) = (bundle_group_id IS NULL) AND (bundle_group_id IS NULL OR promotion_id IS NULL));
CREATE INDEX IF NOT EXISTS idx_order_items_bundle_group ON order_items(store_id, bundle_group_id) WHERE bundle_group_id IS NOT NULL;

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
        'OFFLINE_ORDER_SYNCED', 'OFFLINE_ORDER_REJECTED', 'OFFLINE_ORDER_RESOLVED', 'DEVICE_PIN_FAILED',
        'PROMOTION_CREATED', 'PROMOTION_UPDATED',
        'BUNDLE_CHANGED'
    )
);
