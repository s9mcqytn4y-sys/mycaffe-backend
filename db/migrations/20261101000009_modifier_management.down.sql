-- 20261101000009_modifier_management.down.sql
-- Refuses to run while negative modifier prices exist anywhere, because the restored
-- CHECK constraints would reject them.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM modifier_options WHERE price_delta < 0)
       OR EXISTS (SELECT 1 FROM order_item_options WHERE snapshot_price_delta < 0)
       OR EXISTS (SELECT 1 FROM order_items WHERE modifier_total_price < 0) THEN
        RAISE EXCEPTION 'Cannot restore non-negative modifier price constraints: negative modifier prices exist.';
    END IF;
END $$;

DROP INDEX IF EXISTS uq_modifier_options_group_name_active;
DROP INDEX IF EXISTS uq_modifier_groups_store_name_active;

ALTER TABLE modifier_options DROP COLUMN IF EXISTS archived_at;
ALTER TABLE modifier_groups DROP COLUMN IF EXISTS archived_at;

ALTER TABLE order_items DROP CONSTRAINT IF EXISTS chk_order_items_mod_price;
ALTER TABLE order_items ADD CONSTRAINT chk_order_items_mod_price CHECK (modifier_total_price >= 0);

ALTER TABLE order_item_options DROP CONSTRAINT IF EXISTS chk_oio_price_delta;
ALTER TABLE order_item_options ADD CONSTRAINT chk_oio_price_delta CHECK (snapshot_price_delta >= 0);

ALTER TABLE modifier_options DROP CONSTRAINT IF EXISTS chk_modifier_options_price_delta;
ALTER TABLE modifier_options ADD CONSTRAINT chk_modifier_options_price_delta CHECK (price_delta >= 0);
