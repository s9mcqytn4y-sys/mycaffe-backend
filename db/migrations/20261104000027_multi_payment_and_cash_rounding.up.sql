-- 20261104000027_multi_payment_and_cash_rounding.up.sql
-- v0.8 PR 8.3 (decisions V11, V12): a split may be paid by several direct tenders, and a closing cash
-- tender may be rounded to the store step. Payment amounts stay exact; the rounding is the cash
-- difference, kept per payment and never taxed.

ALTER TABLE payments ADD COLUMN IF NOT EXISTS rounding_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS chk_payments_rounding;
ALTER TABLE payments ADD CONSTRAINT chk_payments_rounding CHECK (
    rounding_amount = 0 OR (method = 'cash' AND abs(rounding_amount) < 500));

ALTER TABLE store_settings ADD COLUMN IF NOT EXISTS cash_rounding_step INT NOT NULL DEFAULT 0;
ALTER TABLE store_settings DROP CONSTRAINT IF EXISTS chk_store_settings_cash_rounding_step;
ALTER TABLE store_settings ADD CONSTRAINT chk_store_settings_cash_rounding_step CHECK (cash_rounding_step IN (0, 100, 500));
