-- 20261104000020_receipt_tokens.down.sql
-- Receipt links carry no financial history (amounts live on the order snapshot), so they are dropped.

DROP TABLE IF EXISTS receipt_tokens;
