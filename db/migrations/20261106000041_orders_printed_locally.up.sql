-- 20261106000041_orders_printed_locally.up.sql
-- v0.10 PR 10.4 (ADR-005, decision W18): an offline order whose kitchen tickets the device already printed
-- on the LAN print agent is synced with printed_locally, and the server queues no print job for it.

ALTER TABLE orders ADD COLUMN IF NOT EXISTS printed_locally BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_printed_locally;
ALTER TABLE orders ADD CONSTRAINT chk_orders_printed_locally CHECK (NOT printed_locally OR origin = 'offline');
