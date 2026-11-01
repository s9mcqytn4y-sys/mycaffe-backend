-- 20261101000015_catalog_variant_pricing.up.sql
-- ADR-004 (approved 2026-11-01): the variant is the only price source. Every product must have one
-- active default variant; products.base_price stays as a deprecated mirror of that variant's price.
-- Unit stock stays on products (owner decision ADR-004 §D3).

-- 1. Products that have active variants but no default: promote the first one.
UPDATE product_variants v
SET is_default = true, updated_at = NOW()
FROM (
    SELECT DISTINCT ON (store_id, product_id) id
    FROM product_variants pv
    WHERE archived_at IS NULL
      AND NOT EXISTS (
          SELECT 1 FROM product_variants d
          WHERE d.store_id = pv.store_id AND d.product_id = pv.product_id AND d.is_default AND d.archived_at IS NULL)
    ORDER BY store_id, product_id, display_order, created_at
) first_variant
WHERE v.id = first_variant.id;

-- 2. Products with no active variant at all: create the default "Reguler" at the legacy base price.
INSERT INTO product_variants (store_id, product_id, name, price, is_default, is_available)
SELECT p.store_id, p.id, 'Reguler', p.base_price, true, true
FROM products p
WHERE NOT EXISTS (
    SELECT 1 FROM product_variants v
    WHERE v.store_id = p.store_id AND v.product_id = p.id AND v.archived_at IS NULL);

COMMENT ON COLUMN products.base_price IS
    'Deprecated (ADR-004): mirror of the default variant price for legacy readers; pricing reads product_variants.price.';

-- 3. A single-choice group allows exactly one selection at most (min <= max is already enforced).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM modifier_groups WHERE selection_type = 'single' AND max_select <> 1) THEN
        RAISE EXCEPTION 'Single-choice modifier groups with max_select <> 1 exist; fix them before migrating.';
    END IF;
END $$;

ALTER TABLE modifier_groups DROP CONSTRAINT IF EXISTS chk_modifier_groups_single_max;
ALTER TABLE modifier_groups
    ADD CONSTRAINT chk_modifier_groups_single_max CHECK (selection_type <> 'single' OR max_select = 1);
