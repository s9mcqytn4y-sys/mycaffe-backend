-- db/migrations/20261028000001_init_schema.down.sql
-- Rollback Teardown Script for MyCaffe Core POS Initial Schema
-- CAUTION: FOR DISPOSABLE LOCAL / CI TEST DATABASES ONLY.
-- DO NOT EXECUTE ON PRODUCTION VPS ENVIRONMENT.

-- Drop triggers
DROP TRIGGER IF EXISTS trg_stores_updated_at ON stores;
DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP TRIGGER IF EXISTS trg_categories_updated_at ON categories;
DROP TRIGGER IF EXISTS trg_products_updated_at ON products;
DROP TRIGGER IF EXISTS trg_orders_updated_at ON orders;
DROP TRIGGER IF EXISTS trg_payments_updated_at ON payments;

-- Drop function
DROP FUNCTION IF EXISTS update_updated_at_column();

-- Drop tables in reverse dependency order
DROP TABLE IF EXISTS audit_logs CASCADE;
DROP TABLE IF EXISTS payment_notification_logs CASCADE;
DROP TABLE IF EXISTS payments CASCADE;
DROP TABLE IF EXISTS order_items CASCADE;
DROP TABLE IF EXISTS orders CASCADE;
DROP TABLE IF EXISTS store_daily_sequences CASCADE;
DROP TABLE IF EXISTS products CASCADE;
DROP TABLE IF EXISTS categories CASCADE;
DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS stores CASCADE;
