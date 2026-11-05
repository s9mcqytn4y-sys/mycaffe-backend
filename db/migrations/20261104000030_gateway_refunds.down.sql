-- 20261104000030_gateway_refunds.down.sql
-- Refuses while gateway refund history exists.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM gateway_refunds) OR EXISTS (SELECT 1 FROM payment_refunds WHERE method = 'gateway') THEN
        RAISE EXCEPTION 'Gateway refund history exists; cannot drop gateway_refunds.';
    END IF;
END $$;

DROP FUNCTION IF EXISTS list_stores_with_open_gateway_refunds(TIMESTAMPTZ);
ALTER TABLE store_settings DROP COLUMN IF EXISTS gateway_partial_refunds;
ALTER TABLE payment_refunds DROP CONSTRAINT IF EXISTS chk_payment_refunds_method;
ALTER TABLE payment_refunds ADD CONSTRAINT chk_payment_refunds_method CHECK (
    (method = 'cash' AND cash_movement_id IS NOT NULL AND bank_name IS NULL AND account_last4 IS NULL AND transfer_reference IS NULL)
    OR (method = 'bank_transfer' AND cash_movement_id IS NULL AND length(trim(bank_name)) > 0
        AND account_last4 ~ '^[0-9]{4}$' AND length(trim(transfer_reference)) > 0)
    OR (method = 'outlet_cash' AND cash_movement_id IS NULL AND bank_name IS NULL AND account_last4 IS NULL AND transfer_reference IS NULL));
DROP TABLE IF EXISTS gateway_refunds;
