-- 20261109000054_stamp_reward_lines.down.sql
-- Refuses while reward lines exist: the stricter check would reject them.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM order_items WHERE promotion_id IS NULL AND promo_discount_amount > 0) THEN
        RAISE EXCEPTION 'Stamp reward lines exist; cannot roll back 000054.';
    END IF;
END $$;

ALTER TABLE order_items DROP CONSTRAINT IF EXISTS chk_order_items_promo;
ALTER TABLE order_items ADD CONSTRAINT chk_order_items_promo CHECK (promo_discount_amount >= 0
    AND (promotion_id IS NULL) = (promo_discount_amount = 0));
