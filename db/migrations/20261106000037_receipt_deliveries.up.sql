-- 20261106000037_receipt_deliveries.up.sql
-- v0.9 PR 9.5 (decision W9): digital receipts sent by e-mail, WhatsApp or SMS. Each send is one row
-- for the rate limits (3 per order, 10 per recipient per day) and the audit trail. The recipient is
-- kept only as a SHA-256 hash and a masked form; the message carries just the receipt link.

CREATE TABLE IF NOT EXISTS receipt_deliveries (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id             UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id             UUID NOT NULL,
    channel              VARCHAR(10) NOT NULL,
    recipient_hash       CHAR(64) NOT NULL,
    recipient_masked     VARCHAR(100) NOT NULL,
    requested_by_user_id UUID,
    guest_session_id     UUID,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_receipt_deliveries_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_receipt_deliveries_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_receipt_deliveries_user FOREIGN KEY (store_id, requested_by_user_id)
        REFERENCES users(store_id, id) ON DELETE SET NULL (requested_by_user_id),
    CONSTRAINT fk_receipt_deliveries_guest FOREIGN KEY (store_id, guest_session_id)
        REFERENCES guest_sessions(store_id, id) ON DELETE SET NULL (guest_session_id),
    CONSTRAINT chk_receipt_deliveries_channel CHECK (channel IN ('email', 'whatsapp', 'sms'))
);
CREATE INDEX IF NOT EXISTS idx_receipt_deliveries_order ON receipt_deliveries(store_id, order_id);
CREATE INDEX IF NOT EXISTS idx_receipt_deliveries_recipient ON receipt_deliveries(store_id, recipient_hash, created_at);

ALTER TABLE receipt_deliveries ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_receipt_deliveries_tenant_isolation ON receipt_deliveries;
CREATE POLICY p_receipt_deliveries_tenant_isolation ON receipt_deliveries FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT ON receipt_deliveries TO mycaffe_app;

ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS chk_audit_logs_action;
ALTER TABLE audit_logs ADD CONSTRAINT chk_audit_logs_action CHECK (
    action IN (
        'VOID_ORDER', 'REFUND', 'STOCK_OVERRIDE', 'PRICE_CHANGE',
        'SHIFT_OPEN', 'SHIFT_CLOSE', 'SHIFT_FORCE_CLOSE',
        'ROLE_CHANGE', 'STATIC_QRIS_RECONCILE', 'CASH_PAYMENT_RECORDED', 'EDC_RECONCILE',
        'ORDER_CREATE', 'ORDER_CANCEL', 'PAYMENT_SETTLED',
        'STAFF_LOGIN_LOCKED', 'STAFF_UNLOCK', 'STAFF_PIN_RESET', 'STAFF_DEACTIVATED',
        'STAFF_SESSION_REVOKED', 'STAFF_INVITED', 'STAFF_PIN_SET', 'STAFF_TEMP_PIN_ISSUED',
        'STORE_BOOTSTRAP', 'STORE_SETTINGS_CHANGED',
        'CASH_MOVEMENT', 'DRAWER_CREATE', 'DRAWER_UPDATE',
        'TAB_OPEN', 'ROUND_SUBMIT', 'TAB_CLOSE', 'TAB_REOPEN',
        'OWNER_PIN_FAILED',
        'TABLE_CREATE', 'TABLE_UPDATE', 'TABLE_QR_ROTATE', 'TABLE_SESSION_CLOSE',
        'PRODUCT_IMAGE_CHANGED',
        'APPROVAL_REQUESTED', 'APPROVAL_APPROVED', 'APPROVAL_REJECTED',
        'STAFF_PHONE_SET', 'CUSTOMER_PHONE_VERIFIED',
        'OUTLET_CASH_MOVEMENT',
        'ORDER_DISCOUNT',
        'STAFF_BADGE_ISSUED', 'STAFF_BADGE_REVOKED',
        'TABLE_MOVE', 'TABLE_MERGE', 'TABLE_SPLIT',
        'TABLE_TIMER_EXTEND',
        'DISPLAY_TOKEN_CREATED', 'DISPLAY_TOKEN_REVOKED',
        'PRINTER_CREATED', 'PRINTER_UPDATED', 'PRINTER_TOKEN_ROTATED', 'PRINT_REPRINT',
        'RECEIPT_SENT'
    )
);
