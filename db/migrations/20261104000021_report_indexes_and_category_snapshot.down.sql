-- 20261104000021_report_indexes_and_category_snapshot.down.sql
-- Dropping the column loses only the category snapshot; reports fall back to nothing until re-applied.

ALTER TABLE order_items DROP COLUMN IF EXISTS snapshot_category_name;
DROP INDEX IF EXISTS idx_cash_movements_store_created_at;
DROP INDEX IF EXISTS idx_orders_store_created_at;
DROP INDEX IF EXISTS idx_payments_store_paid_at;
