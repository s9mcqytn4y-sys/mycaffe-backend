-- 20261107000045_ingredients_recipes.up.sql
-- v0.11 PR 11.3 (K2, decisions X5, X6, X9, X10): ingredients counted in g, ml or pcs (whole units), recipes per
-- variant and per modifier option (an option may substitute: negative quantities), deduction when an order is
-- paid (balances may go negative, X5), an append-only movement ledger, and the cost of goods sold stored per
-- order line at payment (X10). Costs are rupiah per 1000 units so cheap ingredients keep their precision.

CREATE TABLE IF NOT EXISTS ingredients (
    id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id       UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    name           VARCHAR(80) NOT NULL,
    unit           VARCHAR(3) NOT NULL,
    stock_quantity BIGINT NOT NULL DEFAULT 0,
    min_stock      BIGINT NOT NULL DEFAULT 0,
    cost_per_1000  BIGINT NOT NULL DEFAULT 0,
    is_active      BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_ingredients_store_id UNIQUE (store_id, id),
    CONSTRAINT chk_ingredients_name CHECK (length(btrim(name)) > 0),
    CONSTRAINT chk_ingredients_unit CHECK (unit IN ('g', 'ml', 'pcs')),
    CONSTRAINT chk_ingredients_min CHECK (min_stock >= 0),
    CONSTRAINT chk_ingredients_cost CHECK (cost_per_1000 >= 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_ingredients_store_name_active ON ingredients(store_id, lower(name)) WHERE is_active;

CREATE TABLE IF NOT EXISTS recipe_items (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id           UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    variant_id         UUID,
    modifier_option_id UUID,
    ingredient_id      UUID NOT NULL,
    quantity           BIGINT NOT NULL,
    CONSTRAINT fk_recipe_items_variant FOREIGN KEY (store_id, variant_id) REFERENCES product_variants(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_recipe_items_option FOREIGN KEY (store_id, modifier_option_id) REFERENCES modifier_options(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_recipe_items_ingredient FOREIGN KEY (store_id, ingredient_id) REFERENCES ingredients(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_recipe_items_target CHECK ((variant_id IS NULL) <> (modifier_option_id IS NULL)),
    -- A variant uses ingredients; an option may also take some away (X6: oat milk replaces milk).
    CONSTRAINT chk_recipe_items_quantity CHECK (quantity <> 0 AND (variant_id IS NULL OR quantity > 0) AND abs(quantity) <= 100000)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_recipe_items_variant ON recipe_items(store_id, variant_id, ingredient_id) WHERE variant_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_recipe_items_option ON recipe_items(store_id, modifier_option_id, ingredient_id) WHERE modifier_option_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS ingredient_movements (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id      UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    ingredient_id UUID NOT NULL,
    kind          VARCHAR(10) NOT NULL,
    quantity      BIGINT NOT NULL,
    cost_per_1000 BIGINT NOT NULL,
    order_id      UUID,
    actor_id      UUID,
    note          VARCHAR(200),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_ingredient_movements_ingredient FOREIGN KEY (store_id, ingredient_id) REFERENCES ingredients(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_ingredient_movements_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_ingredient_movements_actor FOREIGN KEY (store_id, actor_id) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    -- purchase, count and waste arrive with PRs 11.4 and 11.6.
    CONSTRAINT chk_ingredient_movements_kind CHECK (kind IN ('opening', 'sale', 'purchase', 'count', 'waste')),
    CONSTRAINT chk_ingredient_movements_quantity CHECK (quantity <> 0)
);
CREATE INDEX IF NOT EXISTS idx_ingredient_movements_ingredient ON ingredient_movements(store_id, ingredient_id, created_at);

ALTER TABLE ingredients ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_ingredients_tenant_isolation ON ingredients;
CREATE POLICY p_ingredients_tenant_isolation ON ingredients FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE recipe_items ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_recipe_items_tenant_isolation ON recipe_items;
CREATE POLICY p_recipe_items_tenant_isolation ON recipe_items FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE ingredient_movements ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_ingredient_movements_tenant_isolation ON ingredient_movements;
CREATE POLICY p_ingredient_movements_tenant_isolation ON ingredient_movements FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON ingredients TO mycaffe_app;
-- Recipes are replaced as a whole per variant or option.
GRANT SELECT, INSERT, UPDATE, DELETE ON recipe_items TO mycaffe_app;
-- The ledger is append-only.
GRANT SELECT, INSERT ON ingredient_movements TO mycaffe_app;

-- X9: the unit cost of a ready-to-serve product (moving average once purchasing lands in 11.4).
ALTER TABLE products ADD COLUMN IF NOT EXISTS unit_cost BIGINT NOT NULL DEFAULT 0;
ALTER TABLE products DROP CONSTRAINT IF EXISTS chk_products_unit_cost;
ALTER TABLE products ADD CONSTRAINT chk_products_unit_cost CHECK (unit_cost >= 0);
-- X10: cost of goods sold of the line, stored when the order is paid (NULL before).
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS cogs_amount BIGINT;

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
        'INGREDIENT_CREATED', 'INGREDIENT_UPDATED', 'RECIPE_CHANGED'
    )
);
