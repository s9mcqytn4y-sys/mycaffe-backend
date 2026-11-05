-- 20261104000026_messaging_otp_customers.down.sql
-- Refuses while customers or staff phones exist: verified customers are business data.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM customers) OR EXISTS (SELECT 1 FROM users WHERE phone IS NOT NULL)
       OR EXISTS (SELECT 1 FROM audit_logs WHERE action IN ('STAFF_PHONE_SET', 'CUSTOMER_PHONE_VERIFIED')) THEN
        RAISE EXCEPTION 'Customer or staff phone data exists; cannot drop messaging tables.';
    END IF;
END $$;

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
        'APPROVAL_REQUESTED', 'APPROVAL_APPROVED', 'APPROVAL_REJECTED'
    )
);

DROP FUNCTION IF EXISTS list_stores_with_due_message();
DROP TABLE IF EXISTS message_outbox;
DROP TABLE IF EXISTS otp_challenges;
DROP TABLE IF EXISTS customers;
DROP INDEX IF EXISTS uq_users_store_phone;
ALTER TABLE users DROP CONSTRAINT IF EXISTS chk_users_phone;
ALTER TABLE users DROP COLUMN IF EXISTS phone;
