-- 20261104000027_multi_payment_and_cash_rounding.down.sql
-- Refuses while rounded payments exist: dropping the column would make drawer totals disagree.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payments WHERE rounding_amount <> 0) THEN
        RAISE EXCEPTION 'Rounded cash payments exist; cannot drop payments.rounding_amount.';
    END IF;
END $$;

ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_cash_rounding_step;
ALTER TABLE store_settings DROP COLUMN IF EXISTS cash_rounding_step;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS chk_payments_rounding;
ALTER TABLE payments DROP COLUMN IF EXISTS rounding_amount;
