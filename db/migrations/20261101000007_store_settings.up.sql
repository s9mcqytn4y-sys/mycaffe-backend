-- 20261101000007_store_settings.up.sql
-- Phase 02.01-C: owner-managed operating policies (owner decisions O1, O4, C1, C3, T1, K8).
-- One row per store; a missing row means "defaults" and is created on the first update.

CREATE TABLE IF NOT EXISTS store_settings (
    store_id UUID PRIMARY KEY REFERENCES stores(id) ON DELETE CASCADE,
    void_refund_owner_threshold BIGINT NOT NULL DEFAULT 100000,
    cashier_max_discount_bps BIGINT NOT NULL DEFAULT 1000,
    cash_variance_tolerance BIGINT NOT NULL DEFAULT 5000,
    cashier_handles_bar BOOLEAN NOT NULL DEFAULT false,
    table_idle_close_minutes INT NOT NULL DEFAULT 30,
    qr_ordering_paused BOOLEAN NOT NULL DEFAULT false,
    static_qris_fallback_mode VARCHAR(10) NOT NULL DEFAULT 'auto',
    updated_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_store_settings_updated_by FOREIGN KEY (store_id, updated_by) REFERENCES users(store_id, id) ON DELETE SET NULL (updated_by),
    CONSTRAINT chk_store_settings_void_threshold CHECK (void_refund_owner_threshold >= 0),
    CONSTRAINT chk_store_settings_discount_bps CHECK (cashier_max_discount_bps BETWEEN 0 AND 10000),
    CONSTRAINT chk_store_settings_cash_variance CHECK (cash_variance_tolerance >= 0),
    CONSTRAINT chk_store_settings_idle_minutes CHECK (table_idle_close_minutes BETWEEN 5 AND 240),
    CONSTRAINT chk_store_settings_qris_fallback CHECK (static_qris_fallback_mode IN ('off', 'auto', 'forced'))
);

CREATE TRIGGER trg_store_settings_updated_at
BEFORE UPDATE ON store_settings
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE store_settings ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS store_isolation_store_settings ON store_settings;
CREATE POLICY store_isolation_store_settings ON store_settings FOR ALL
    USING (store_id = current_store_id())
    WITH CHECK (store_id = current_store_id());

GRANT SELECT, INSERT, UPDATE ON store_settings TO mycaffe_app;

ALTER TABLE audit_logs
    DROP CONSTRAINT IF EXISTS chk_audit_logs_action;

ALTER TABLE audit_logs
    ADD CONSTRAINT chk_audit_logs_action
    CHECK (action IN (
        'VOID_ORDER',
        'REFUND',
        'STOCK_OVERRIDE',
        'PRICE_CHANGE',
        'SHIFT_CLOSE',
        'ROLE_CHANGE',
        'STATIC_QRIS_RECONCILE',
        'CASH_PAYMENT_RECORDED',
        'EDC_RECONCILE',
        'STAFF_LOGIN_LOCKED',
        'STAFF_SESSION_REVOKED',
        'STAFF_UNLOCK',
        'STAFF_PIN_RESET',
        'STORE_BOOTSTRAP',
        'STAFF_INVITED',
        'STAFF_PIN_SET',
        'STAFF_TEMP_PIN_ISSUED',
        'STORE_SETTINGS_CHANGED'
    ));
