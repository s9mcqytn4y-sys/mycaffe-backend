-- 20261109000054_stamp_reward_lines.up.sql
-- v0.11.1 PR 11.8 (X17): a stamp card reward is a line discount without a promotion row. Such a discount must
-- still carry a name, so a line can never lose money without saying why on the receipt.

ALTER TABLE order_items DROP CONSTRAINT IF EXISTS chk_order_items_promo;
ALTER TABLE order_items ADD CONSTRAINT chk_order_items_promo CHECK (promo_discount_amount >= 0
    AND ((promotion_id IS NULL) = (promo_discount_amount = 0)
         OR (promotion_id IS NULL AND promo_discount_amount > 0 AND snapshot_promo_name IS NOT NULL)));
