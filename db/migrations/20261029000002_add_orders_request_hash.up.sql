-- db/migrations/20261029000002_add_orders_request_hash.up.sql
-- Additive migration: adds request_hash to orders for deterministic idempotency conflict detection.
-- Preserves SHA-256 checksum of 20261028000001_init_schema.up.sql.

ALTER TABLE orders
ADD COLUMN IF NOT EXISTS request_hash VARCHAR(64) NOT NULL DEFAULT '';

