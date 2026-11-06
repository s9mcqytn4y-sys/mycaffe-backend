-- 20261106000042_device_pending_shifts.down.sql
-- Pending reports are transient (refreshed on every sync), so dropping them loses no business record.

DROP INDEX IF EXISTS idx_devices_pending_shifts;
ALTER TABLE devices DROP COLUMN IF EXISTS pending_shift_ids;
