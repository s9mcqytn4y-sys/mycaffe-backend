-- Iteration 4d: Void, Refund, and Stock Override Supporting Indexes
-- PostgreSQL 18 Compatible

CREATE INDEX IF NOT EXISTS idx_audit_logs_store_action_ref 
    ON audit_logs(store_id, action, reference_id);

CREATE INDEX IF NOT EXISTS idx_cash_movements_store_shift_type 
    ON cash_movements(store_id, shift_id, movement_type);

