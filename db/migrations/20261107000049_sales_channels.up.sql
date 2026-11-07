-- 20261107000049_sales_channels.up.sql
-- v0.11.1 PR 11.7 (X12-X15): sales channels for delivery-app orders entered by hand at the POS. A channel has
-- its own price per variant (variants without one keep their normal price) and a commission estimate. Channel
-- orders are paid with the new `platform` method, which never touches the cash drawer.

CREATE TABLE IF NOT EXISTS sales_channels (
    id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id       UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    name           VARCHAR(50) NOT NULL,
    commission_bps INT NOT NULL DEFAULT 0,
    is_active      BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_sales_channels_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_sales_channels_name UNIQUE (store_id, name),
    CONSTRAINT chk_sales_channels_commission CHECK (commission_bps BETWEEN 0 AND 10000)
);

CREATE TABLE IF NOT EXISTS channel_prices (
    store_id         UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    sales_channel_id UUID NOT NULL,
    variant_id       UUID NOT NULL,
    price            BIGINT NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (store_id, sales_channel_id, variant_id),
    CONSTRAINT fk_channel_prices_channel FOREIGN KEY (store_id, sales_channel_id) REFERENCES sales_channels(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_channel_prices_variant FOREIGN KEY (store_id, variant_id) REFERENCES product_variants(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_channel_prices_price CHECK (price >= 0)
);

-- The channel and its commission are fixed when the order is created, so reports do not move when it changes.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS sales_channel_id UUID;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS snapshot_channel_name VARCHAR(50);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS channel_commission_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_sales_channel;
ALTER TABLE orders ADD CONSTRAINT fk_orders_sales_channel FOREIGN KEY (store_id, sales_channel_id) REFERENCES sales_channels(store_id, id) ON DELETE RESTRICT;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_channel_commission;
ALTER TABLE orders ADD CONSTRAINT chk_orders_channel_commission CHECK (channel_commission_amount >= 0
    AND (sales_channel_id IS NOT NULL OR channel_commission_amount = 0));

ALTER TABLE payments DROP CONSTRAINT IF EXISTS chk_payments_method;
ALTER TABLE payments ADD CONSTRAINT chk_payments_method CHECK (method IN ('qris', 'static_qris', 'bank_transfer', 'gopay', 'shopeepay', 'card', 'cstore', 'cash', 'edc', 'platform'));

ALTER TABLE sales_channels ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_sales_channels_tenant_isolation ON sales_channels;
CREATE POLICY p_sales_channels_tenant_isolation ON sales_channels FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE channel_prices ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_channel_prices_tenant_isolation ON channel_prices;
CREATE POLICY p_channel_prices_tenant_isolation ON channel_prices FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
-- Channels are deactivated, never deleted; a channel price is removed to fall back to the normal price.
GRANT SELECT, INSERT, UPDATE ON sales_channels TO mycaffe_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON channel_prices TO mycaffe_app;

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
        'BUNDLE_CHANGED',
        'INGREDIENT_CREATED', 'INGREDIENT_UPDATED', 'RECIPE_CHANGED',
        'SUPPLIER_CHANGED', 'PURCHASE_ORDER_CHANGED', 'GOODS_RECEIVED',
        'PAYABLE_PAID',
        'STOCK_SESSION_SUBMITTED', 'STOCK_SESSION_APPROVED', 'STOCK_SESSION_REJECTED',
        'SALES_CHANNEL_CHANGED', 'CHANNEL_PRICES_CHANGED', 'PLATFORM_PAYMENT_RECORDED'
    )
);
