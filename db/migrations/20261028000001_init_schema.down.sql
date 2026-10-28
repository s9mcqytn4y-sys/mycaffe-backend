-- db/migrations/20261028000001_init_schema.down.sql
-- Rollback / Teardown Migration for MyCaffe Core POS Initial Schema
-- WARNING: Execute ONLY on disposable CI/local test databases. Never run on production data.

DROP TRIGGER IF EXISTS trg_payments_updated_at ON payments;
DROP TRIGGER IF EXISTS trg_payment_splits_updated_at ON payment_splits;
DROP TRIGGER IF EXISTS trg_orders_updated_at ON orders;
DROP TRIGGER IF EXISTS trg_modifier_options_updated_at ON modifier_options;
DROP TRIGGER IF EXISTS trg_modifier_groups_updated_at ON modifier_groups;
DROP TRIGGER IF EXISTS trg_products_updated_at ON products;
DROP TRIGGER IF EXISTS trg_categories_updated_at ON categories;
DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP TRIGGER IF EXISTS trg_stores_updated_at ON stores;

DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS payment_notification_logs;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS payment_split_items;
DROP TABLE IF EXISTS payment_splits;
DROP TABLE IF EXISTS order_item_options;
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS store_daily_sequences;
DROP TABLE IF EXISTS option_modifier_groups;
DROP TABLE IF EXISTS product_modifier_groups;
DROP TABLE IF EXISTS modifier_options;
DROP TABLE IF EXISTS modifier_groups;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS categories;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS stores;

DROP FUNCTION IF EXISTS current_store_id();
DROP FUNCTION IF EXISTS update_updated_at_column();
