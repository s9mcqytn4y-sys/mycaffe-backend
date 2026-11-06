-- 20261106000040_negative_stock_offline.down.sql
-- Refuses while a balance is negative or a line is flagged oversold: restoring the CHECK would fail or
-- erase the record of an oversale. Correct the stock with a stock override first.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM products WHERE stock_quantity < 0) OR EXISTS (SELECT 1 FROM modifier_options WHERE stock_quantity < 0)
       OR EXISTS (SELECT 1 FROM order_items WHERE oversold) THEN
        RAISE EXCEPTION 'Negative stock or oversold lines exist; cannot roll back 000040.';
    END IF;
END $$;

ALTER TABLE order_items DROP COLUMN IF EXISTS oversold;
ALTER TABLE modifier_options DROP CONSTRAINT IF EXISTS chk_modifier_options_stock;
ALTER TABLE modifier_options ADD CONSTRAINT chk_modifier_options_stock CHECK (stock_quantity >= 0 AND max_quantity >= 1);
ALTER TABLE products ADD CONSTRAINT chk_products_stock_quantity CHECK (stock_quantity >= 0);
