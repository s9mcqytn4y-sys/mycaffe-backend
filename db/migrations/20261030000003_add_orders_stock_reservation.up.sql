-- db/migrations/20261030000003_add_orders_stock_reservation.up.sql
-- Additive migration for Phase 01 Sub-task 03 Hardening:
-- 1. Adds stock_status ('not_tracked', 'reserved', 'committed', 'released') to orders for safe unpaid stock reservation and idempotent release.
-- 2. Adds expires_at TIMESTAMPTZ to orders for unpaid reservation expiry tracking.
-- 3. Adds created_by_user_id UUID to orders with composite tenant FK to verify staff authorization on open_tab orders.

ALTER TABLE orders ADD COLUMN IF NOT EXISTS stock_status VARCHAR(20) NOT NULL DEFAULT 'reserved';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ DEFAULT NULL;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS created_by_user_id UUID DEFAULT NULL;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_stock_status;
ALTER TABLE orders ADD CONSTRAINT chk_orders_stock_status CHECK (stock_status IN ('not_tracked', 'reserved', 'committed', 'released'));

ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_created_by_user;
ALTER TABLE orders ADD CONSTRAINT fk_orders_created_by_user FOREIGN KEY (store_id, created_by_user_id) REFERENCES users(store_id, id) ON DELETE SET NULL (created_by_user_id);

CREATE INDEX IF NOT EXISTS idx_orders_store_stock_reservation_expiry
ON orders(store_id, status, stock_status, expires_at)
WHERE status = 'pending_payment' AND stock_status = 'reserved';
