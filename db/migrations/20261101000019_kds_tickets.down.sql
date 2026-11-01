-- 20261101000019_kds_tickets.down.sql
-- Refuses while tickets exist: preparation history (SLA timestamps, late-payment flags) is never dropped.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM kds_tickets) THEN
        RAISE EXCEPTION 'KDS tickets exist; cannot drop kds_tickets.';
    END IF;
END $$;

DROP TABLE IF EXISTS kds_tickets;
ALTER TABLE order_items DROP CONSTRAINT IF EXISTS chk_order_items_prep_station;
ALTER TABLE order_items DROP COLUMN IF EXISTS snapshot_prep_station;
