-- db/migrations/20261030000003_add_orders_stock_reservation.down.sql
-- Rollback for 20261030000003_add_orders_stock_reservation.up.sql

DROP INDEX IF EXISTS idx_orders_store_stock_reservation_expiry;

ALTER TABLE orders
DROP CONSTRAINT IF EXISTS fk_orders_created_by_user;

ALTER TABLE orders
DROP CONSTRAINT IF EXISTS chk_orders_stock_status;

ALTER TABLE orders
DROP COLUMN IF EXISTS created_by_user_id,
DROP COLUMN IF EXISTS expires_at,
DROP COLUMN IF EXISTS stock_status;
