-- ==============================================================================
-- Rollback Teardown Script for MyCaffe POS Initial Schema
-- For DISPOSABLE test / local environments ONLY.
-- DO NOT RUN ON PRODUCTION.
-- ==============================================================================

-- Drop tables in reverse order of foreign key dependencies
DROP TABLE IF EXISTS payment_notification_logs CASCADE;
DROP TABLE IF EXISTS payments CASCADE;
DROP TABLE IF EXISTS order_item_modifiers CASCADE;
DROP TABLE IF EXISTS order_items CASCADE;
DROP TABLE IF EXISTS orders CASCADE;
DROP TABLE IF EXISTS store_daily_sequences CASCADE;
DROP TABLE IF EXISTS product_modifiers CASCADE;
DROP TABLE IF EXISTS modifier_options CASCADE;
DROP TABLE IF EXISTS modifier_groups CASCADE;
DROP TABLE IF EXISTS products CASCADE;
DROP TABLE IF EXISTS categories CASCADE;
DROP TABLE IF EXISTS tables CASCADE;
DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS stores CASCADE;

-- Drop trigger function
DROP FUNCTION IF EXISTS update_updated_at_column CASCADE;
