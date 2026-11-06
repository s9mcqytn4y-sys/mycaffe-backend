-- 20261106000040_negative_stock_offline.up.sql
-- v0.10 PR 10.3 (ADR-005 OF5, decision W17): tracked stock may go below zero, but only through offline
-- sync. Online ordering keeps its guarded deduction (stock_quantity >= qty) and still refuses short stock.
-- Lines whose offline sale drove a balance below zero are flagged oversold. Readers of stock_quantity were
-- audited before this change (catalog availability treats <= 0 as sold out; owner input stays >= 0).

ALTER TABLE products DROP CONSTRAINT IF EXISTS chk_products_stock_quantity;
ALTER TABLE modifier_options DROP CONSTRAINT IF EXISTS chk_modifier_options_stock;
ALTER TABLE modifier_options ADD CONSTRAINT chk_modifier_options_stock CHECK (max_quantity >= 1);

ALTER TABLE order_items ADD COLUMN IF NOT EXISTS oversold BOOLEAN NOT NULL DEFAULT false;
