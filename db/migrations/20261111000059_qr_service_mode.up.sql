-- 20261111000059_qr_service_mode.up.sql
-- PRD v3 Q3: how a QR order reaches the guest. 'pickup' (default): the guest collects it at the bar when
-- it is ready. 'table': staff bring it to the table and mark it served. Only the wording, notices and
-- sound on the guest page change; order statuses stay the same.

ALTER TABLE store_settings
    ADD COLUMN IF NOT EXISTS qr_service_mode VARCHAR(10) NOT NULL DEFAULT 'pickup';
ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_qr_service_mode;
ALTER TABLE store_settings ADD CONSTRAINT chk_store_settings_qr_service_mode
    CHECK (qr_service_mode IN ('pickup', 'table'));
