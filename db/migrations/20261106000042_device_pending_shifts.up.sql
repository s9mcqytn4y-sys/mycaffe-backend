-- 20261106000042_device_pending_shifts.up.sql
-- v0.10 PR 10.5 (ADR-005 OF4): each sync reports the shifts that still have orders waiting on the device.
-- A cashier cannot close such a shift (the owner can force-close it with a reason). Orders that arrive
-- after their shift was closed still go to it; its Z-report is shown as amended and keeps the original
-- close figures (derived from the late offline payments, no extra column).

ALTER TABLE devices ADD COLUMN IF NOT EXISTS pending_shift_ids UUID[] NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_devices_pending_shifts ON devices USING GIN (pending_shift_ids) WHERE revoked_at IS NULL;
