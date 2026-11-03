-- 20261104000021_report_indexes_and_category_snapshot.up.sql
-- B2 owner reports: date-range indexes for read-only report queries, and the product category
-- snapshotted on each order line so moving a product to another category does not rewrite history.

CREATE INDEX IF NOT EXISTS idx_payments_store_paid_at ON payments(store_id, paid_at) WHERE paid_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_orders_store_created_at ON orders(store_id, created_at);
CREATE INDEX IF NOT EXISTS idx_cash_movements_store_created_at ON cash_movements(store_id, created_at);

ALTER TABLE order_items ADD COLUMN IF NOT EXISTS snapshot_category_name VARCHAR(100) NOT NULL DEFAULT '';

-- Lines sold before this migration get the category their product has today (the only approximation;
-- lines of products without a category stay empty and report as "Tanpa kategori").
UPDATE order_items oi SET snapshot_category_name = c.name
FROM products p JOIN categories c ON c.store_id = p.store_id AND c.id = p.category_id
WHERE oi.store_id = p.store_id AND oi.product_id = p.id AND oi.snapshot_category_name = '';
