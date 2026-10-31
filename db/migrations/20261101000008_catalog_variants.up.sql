-- 20261101000008_catalog_variants.up.sql
-- Iteration 3 / catalog C2 (owner decisions K1, K2): product type, preparation station, archiving,
-- and sellable variants with their own SKU and price. Existing products get one default variant.

ALTER TABLE products
    ADD COLUMN IF NOT EXISTS product_type VARCHAR(20) NOT NULL DEFAULT 'prepared',
    ADD COLUMN IF NOT EXISTS prep_station VARCHAR(10) NOT NULL DEFAULT 'bar',
    ADD COLUMN IF NOT EXISTS display_order INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

ALTER TABLE products
    DROP CONSTRAINT IF EXISTS chk_products_type,
    DROP CONSTRAINT IF EXISTS chk_products_prep_station;

ALTER TABLE products
    ADD CONSTRAINT chk_products_type CHECK (product_type IN ('prepared', 'ready_to_serve')),
    ADD CONSTRAINT chk_products_prep_station CHECK (prep_station IN ('bar', 'kitchen', 'none'));

-- Product names are unique among non-archived products of a store (case-insensitive).
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM products WHERE archived_at IS NULL
        GROUP BY store_id, lower(name) HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'Duplicate product names (case-insensitive) exist in a store; rename them before migrating.';
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_products_store_name_active
    ON products(store_id, lower(name)) WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS product_variants (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    product_id UUID NOT NULL,
    name VARCHAR(60) NOT NULL,
    sku VARCHAR(64) NOT NULL DEFAULT '',
    price BIGINT NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT false,
    is_available BOOLEAN NOT NULL DEFAULT true,
    display_order INT NOT NULL DEFAULT 0,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_product_variants_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_product_variants_product FOREIGN KEY (store_id, product_id) REFERENCES products(store_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_product_variants_price CHECK (price >= 0),
    CONSTRAINT chk_product_variants_name CHECK (length(btrim(name)) > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_product_variants_sku
    ON product_variants(store_id, upper(sku)) WHERE sku <> '' AND archived_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_variants_name
    ON product_variants(store_id, product_id, lower(name)) WHERE archived_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_variants_default
    ON product_variants(store_id, product_id) WHERE is_default AND archived_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_product_variants_product
    ON product_variants(store_id, product_id, display_order);

CREATE TRIGGER trg_product_variants_updated_at
BEFORE UPDATE ON product_variants
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE product_variants ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS store_isolation_product_variants ON product_variants;
CREATE POLICY store_isolation_product_variants ON product_variants FOR ALL
    USING (store_id = current_store_id())
    WITH CHECK (store_id = current_store_id());

-- Catalog rows are archived, never deleted, by the runtime role once orders reference them.
GRANT SELECT, INSERT, UPDATE ON product_variants TO mycaffe_app;

-- Every existing product becomes sellable through one default variant at its base price.
INSERT INTO product_variants (store_id, product_id, name, price, is_default, is_available)
SELECT p.store_id, p.id, 'Reguler', p.base_price, true, true
FROM products p
WHERE NOT EXISTS (SELECT 1 FROM product_variants v WHERE v.store_id = p.store_id AND v.product_id = p.id);

-- Order lines snapshot the variant so later catalog edits never rewrite receipts.
ALTER TABLE order_items
    ADD COLUMN IF NOT EXISTS variant_id UUID,
    ADD COLUMN IF NOT EXISTS snapshot_variant_name VARCHAR(60) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS snapshot_sku VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE order_items
    DROP CONSTRAINT IF EXISTS fk_order_items_variant;

ALTER TABLE order_items
    ADD CONSTRAINT fk_order_items_variant FOREIGN KEY (store_id, variant_id)
        REFERENCES product_variants(store_id, id) ON DELETE SET NULL (variant_id);
