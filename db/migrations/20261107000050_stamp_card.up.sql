-- 20261107000050_stamp_card.up.sql
-- v0.11.1 PR 11.8 (X16, X17): digital stamp card. Orders may name a verified customer; a paid direct order at
-- or above the minimum earns one stamp that expires after the program's validity. N live stamps buy one free
-- unit of an owner-chosen product, redeemed when the reward order is paid. A stamp whose order was fully
-- refunded stops counting (read through orders.payment_status), so refunds need no extra write.

CREATE TABLE IF NOT EXISTS stamp_programs (
    store_id           UUID PRIMARY KEY REFERENCES stores(id) ON DELETE RESTRICT,
    is_active          BOOLEAN NOT NULL DEFAULT false,
    stamps_required    INT NOT NULL DEFAULT 10,
    min_order_amount   BIGINT NOT NULL DEFAULT 25000,
    stamp_valid_days   INT NOT NULL DEFAULT 180,
    reward_product_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_stamp_programs_required CHECK (stamps_required BETWEEN 2 AND 50),
    CONSTRAINT chk_stamp_programs_min CHECK (min_order_amount >= 0),
    CONSTRAINT chk_stamp_programs_valid_days CHECK (stamp_valid_days BETWEEN 1 AND 730),
    CONSTRAINT chk_stamp_programs_rewards CHECK (jsonb_typeof(reward_product_ids) = 'array')
);

ALTER TABLE orders ADD COLUMN IF NOT EXISTS customer_id UUID;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS stamp_reward BOOLEAN NOT NULL DEFAULT false;
-- Set when the reward order was paid after its stamps were spent elsewhere: it keeps its free item (X3 style).
ALTER TABLE orders ADD COLUMN IF NOT EXISTS stamp_reward_unbacked BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_customer;
ALTER TABLE orders ADD CONSTRAINT fk_orders_customer FOREIGN KEY (store_id, customer_id) REFERENCES customers(store_id, id) ON DELETE RESTRICT;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_stamp_reward;
ALTER TABLE orders ADD CONSTRAINT chk_orders_stamp_reward CHECK (NOT stamp_reward OR customer_id IS NOT NULL);
CREATE INDEX IF NOT EXISTS idx_orders_customer ON orders(store_id, customer_id) WHERE customer_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS customer_stamps (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id          UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    customer_id       UUID NOT NULL,
    order_id          UUID NOT NULL,
    earned_at         TIMESTAMPTZ NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    redeemed_order_id UUID,
    redeemed_at       TIMESTAMPTZ,
    CONSTRAINT uq_customer_stamps_order UNIQUE (store_id, order_id),
    CONSTRAINT fk_customer_stamps_customer FOREIGN KEY (store_id, customer_id) REFERENCES customers(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_customer_stamps_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_customer_stamps_redeemed FOREIGN KEY (store_id, redeemed_order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_customer_stamps_expiry CHECK (expires_at > earned_at),
    CONSTRAINT chk_customer_stamps_redeemed CHECK ((redeemed_order_id IS NULL) = (redeemed_at IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_customer_stamps_live ON customer_stamps(store_id, customer_id, earned_at) WHERE redeemed_order_id IS NULL;

ALTER TABLE stamp_programs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_stamp_programs_tenant_isolation ON stamp_programs;
CREATE POLICY p_stamp_programs_tenant_isolation ON stamp_programs FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE customer_stamps ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_customer_stamps_tenant_isolation ON customer_stamps;
CREATE POLICY p_customer_stamps_tenant_isolation ON customer_stamps FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
-- Stamps are only ever marked redeemed, never deleted.
GRANT SELECT, INSERT, UPDATE ON stamp_programs TO mycaffe_app;
GRANT SELECT, INSERT, UPDATE ON customer_stamps TO mycaffe_app;

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
        'SALES_CHANNEL_CHANGED', 'CHANNEL_PRICES_CHANGED', 'PLATFORM_PAYMENT_RECORDED',
        'STAMP_PROGRAM_CHANGED', 'STAMP_REWARD_REDEEMED'
    )
);
