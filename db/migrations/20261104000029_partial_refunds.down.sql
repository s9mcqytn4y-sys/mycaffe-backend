-- 20261104000029_partial_refunds.down.sql
-- Refuses while partial or outlet-cash refunds exist: restoring one refund per payment would lie.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payment_refund_lines)
       OR EXISTS (SELECT 1 FROM payment_refunds WHERE method = 'outlet_cash')
       OR EXISTS (SELECT store_id, payment_id FROM payment_refunds GROUP BY store_id, payment_id HAVING COUNT(*) > 1) THEN
        RAISE EXCEPTION 'Partial or outlet-cash refunds exist; cannot restore one refund per payment.';
    END IF;
END $$;

DROP TABLE IF EXISTS payment_refund_lines;
ALTER TABLE payment_refunds DROP CONSTRAINT IF EXISTS chk_payment_refunds_method;
ALTER TABLE payment_refunds ADD CONSTRAINT chk_payment_refunds_method CHECK (
    (method = 'cash' AND cash_movement_id IS NOT NULL AND bank_name IS NULL AND account_last4 IS NULL AND transfer_reference IS NULL)
    OR (method = 'bank_transfer' AND cash_movement_id IS NULL AND length(trim(bank_name)) > 0
        AND account_last4 ~ '^[0-9]{4}$' AND length(trim(transfer_reference)) > 0));
DROP INDEX IF EXISTS idx_payment_refunds_payment;
DROP INDEX IF EXISTS uq_payment_refunds_idempotency;
ALTER TABLE payment_refunds DROP COLUMN IF EXISTS idempotency_key;
ALTER TABLE payment_refunds ADD CONSTRAINT uq_payment_refunds_payment UNIQUE (store_id, payment_id);
