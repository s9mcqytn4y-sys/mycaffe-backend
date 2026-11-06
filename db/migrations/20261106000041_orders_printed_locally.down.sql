-- 20261106000041_orders_printed_locally.down.sql
-- Refuses while an order is marked printed_locally: dropping the flag would let a reprint path forget it.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM orders WHERE printed_locally) THEN
        RAISE EXCEPTION 'Orders printed locally exist; cannot roll back 000041.';
    END IF;
END $$;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_printed_locally;
ALTER TABLE orders DROP COLUMN IF EXISTS printed_locally;
