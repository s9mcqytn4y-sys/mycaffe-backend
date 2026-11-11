-- 20261111000059_qr_service_mode.down.sql
ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_qr_service_mode;
ALTER TABLE store_settings DROP COLUMN IF EXISTS qr_service_mode;
