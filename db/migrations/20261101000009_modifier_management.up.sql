-- 20261101000009_modifier_management.up.sql
-- Iteration 4a (owner decision P1): negative modifier price deltas ("Tanpa susu −Rp2.000") and
-- archiving of modifier groups/options for the management API. A line's unit price
-- (order_items.snapshot_price) still must stay >= 0; that CHECK is unchanged.

ALTER TABLE modifier_options
    DROP CONSTRAINT IF EXISTS chk_modifier_options_price_delta;
ALTER TABLE modifier_options
    ADD CONSTRAINT chk_modifier_options_price_delta CHECK (price_delta BETWEEN -100000000 AND 100000000);

ALTER TABLE order_item_options
    DROP CONSTRAINT IF EXISTS chk_oio_price_delta;
ALTER TABLE order_item_options
    ADD CONSTRAINT chk_oio_price_delta CHECK (snapshot_price_delta BETWEEN -100000000 AND 100000000);

ALTER TABLE order_items
    DROP CONSTRAINT IF EXISTS chk_order_items_mod_price;
ALTER TABLE order_items
    ADD CONSTRAINT chk_order_items_mod_price CHECK (modifier_total_price BETWEEN -100000000000 AND 100000000000);

ALTER TABLE modifier_groups
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
ALTER TABLE modifier_options
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM modifier_groups GROUP BY store_id, lower(name) HAVING COUNT(*) > 1) THEN
        RAISE EXCEPTION 'Duplicate modifier group names (case-insensitive) exist in a store; rename them before migrating.';
    END IF;
    IF EXISTS (SELECT 1 FROM modifier_options GROUP BY store_id, group_id, lower(name) HAVING COUNT(*) > 1) THEN
        RAISE EXCEPTION 'Duplicate modifier option names within a group exist; rename them before migrating.';
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_modifier_groups_store_name_active
    ON modifier_groups(store_id, lower(name)) WHERE archived_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_modifier_options_group_name_active
    ON modifier_options(store_id, group_id, lower(name)) WHERE archived_at IS NULL;
