-- Rollback: 20261101000013_void_refund_stock_override.down.sql

DROP INDEX IF EXISTS idx_cash_movements_store_shift_type;
DROP INDEX IF EXISTS idx_audit_logs_store_action_ref;

