-- 20261101000015_catalog_variant_pricing.down.sql
-- Backfilled/promoted default variants are kept: they are valid catalog data that orders may reference.

ALTER TABLE modifier_groups DROP CONSTRAINT IF EXISTS chk_modifier_groups_single_max;
COMMENT ON COLUMN products.base_price IS NULL;
