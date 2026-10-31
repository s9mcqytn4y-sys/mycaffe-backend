-- 20261101000008_catalog_variants.down.sql
-- Variant snapshots on order_items are dropped; product base_price is kept, so historical
-- receipts keep their snapshot_name/snapshot_price.

ALTER TABLE order_items
    DROP CONSTRAINT IF EXISTS fk_order_items_variant;

ALTER TABLE order_items
    DROP COLUMN IF EXISTS snapshot_sku,
    DROP COLUMN IF EXISTS snapshot_variant_name,
    DROP COLUMN IF EXISTS variant_id;

DROP TABLE IF EXISTS product_variants;

DROP INDEX IF EXISTS uq_products_store_name_active;

ALTER TABLE products
    DROP CONSTRAINT IF EXISTS chk_products_prep_station,
    DROP CONSTRAINT IF EXISTS chk_products_type;

ALTER TABLE products
    DROP COLUMN IF EXISTS archived_at,
    DROP COLUMN IF EXISTS display_order,
    DROP COLUMN IF EXISTS prep_station,
    DROP COLUMN IF EXISTS product_type;
