-- 20261104000023_payment_refunds.down.sql
-- Refuses while refund ledger rows exist: refund history is never dropped.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payment_refunds) THEN
        RAISE EXCEPTION 'payment_refunds has rows; refund history cannot be dropped.';
    END IF;
END $$;

ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_refund_window;
ALTER TABLE store_settings DROP COLUMN IF EXISTS refund_window_owner_days;
ALTER TABLE store_settings DROP COLUMN IF EXISTS refund_window_cashier_days;
DROP TABLE IF EXISTS payment_refunds;
ALTER TABLE cash_movements DROP CONSTRAINT IF EXISTS uq_cash_movements_store_id;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS uq_payments_store_id;
