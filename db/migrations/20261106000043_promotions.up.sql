-- 20261106000043_promotions.up.sql
-- v0.11 PR 11.1 (K7, decisions X2/X3): owner-defined automatic promotions. A promotion takes a percent
-- (bps) or a rupiah amount per unit off matching lines (products and/or categories), optionally within a
-- period, on some weekdays and between two times of day (happy hour, store time zone), and up to a quota of
-- paid orders. One best promotion per line, never stacked. The line subtotal stores the price after the
-- promotion, so the cashier discount, service, tax, refunds and reports all work on it (X2).

CREATE TABLE IF NOT EXISTS promotions (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id      UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    name          VARCHAR(80) NOT NULL,
    kind          VARCHAR(10) NOT NULL,
    value         BIGINT NOT NULL,
    product_ids   JSONB NOT NULL DEFAULT '[]',
    category_ids  JSONB NOT NULL DEFAULT '[]',
    starts_at     TIMESTAMPTZ,
    ends_at       TIMESTAMPTZ,
    days_mask     SMALLINT NOT NULL DEFAULT 127,
    start_minute  SMALLINT,
    end_minute    SMALLINT,
    quota         INT,
    used_count    INT NOT NULL DEFAULT 0,
    is_active     BOOLEAN NOT NULL DEFAULT true,
    created_by    UUID NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_promotions_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_promotions_creator FOREIGN KEY (store_id, created_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_promotions_name CHECK (length(btrim(name)) > 0),
    CONSTRAINT chk_promotions_value CHECK ((kind = 'percent' AND value BETWEEN 1 AND 10000) OR (kind = 'amount' AND value BETWEEN 1 AND 100000000)),
    CONSTRAINT chk_promotions_targets CHECK (jsonb_typeof(product_ids) = 'array' AND jsonb_typeof(category_ids) = 'array'
        AND jsonb_array_length(product_ids) + jsonb_array_length(category_ids) BETWEEN 1 AND 200),
    CONSTRAINT chk_promotions_period CHECK (starts_at IS NULL OR ends_at IS NULL OR ends_at > starts_at),
    CONSTRAINT chk_promotions_days CHECK (days_mask BETWEEN 1 AND 127),
    CONSTRAINT chk_promotions_hours CHECK ((start_minute IS NULL) = (end_minute IS NULL)
        AND (start_minute IS NULL OR (start_minute BETWEEN 0 AND 1439 AND end_minute BETWEEN 1 AND 1440 AND end_minute <> start_minute))),
    CONSTRAINT chk_promotions_quota CHECK (quota IS NULL OR quota >= 1),
    CONSTRAINT chk_promotions_used CHECK (used_count >= 0)
);
CREATE INDEX IF NOT EXISTS idx_promotions_active ON promotions(store_id) WHERE is_active;

ALTER TABLE promotions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_promotions_tenant_isolation ON promotions;
CREATE POLICY p_promotions_tenant_isolation ON promotions FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON promotions TO mycaffe_app;

ALTER TABLE order_items
    ADD COLUMN IF NOT EXISTS promotion_id UUID,
    ADD COLUMN IF NOT EXISTS promo_discount_amount BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS snapshot_promo_name VARCHAR(80);
ALTER TABLE order_items DROP CONSTRAINT IF EXISTS fk_order_items_promotion;
ALTER TABLE order_items ADD CONSTRAINT fk_order_items_promotion FOREIGN KEY (store_id, promotion_id) REFERENCES promotions(store_id, id) ON DELETE RESTRICT;
ALTER TABLE order_items DROP CONSTRAINT IF EXISTS chk_order_items_promo;
ALTER TABLE order_items ADD CONSTRAINT chk_order_items_promo CHECK (promo_discount_amount >= 0
    AND (promotion_id IS NULL) = (promo_discount_amount = 0));

-- X3: a promotion whose quota was already used up when the order was paid keeps its price and is flagged.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS promo_over_quota BOOLEAN NOT NULL DEFAULT false;

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
        'PROMOTION_CREATED', 'PROMOTION_UPDATED'
    )
);
