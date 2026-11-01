-- db/migrations/20261029000002_add_orders_request_hash.down.sql
-- Rollback for 20261029000002_add_orders_request_hash.up.sql

ALTER TABLE orders
DROP COLUMN IF EXISTS request_hash;

