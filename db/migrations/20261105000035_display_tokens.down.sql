-- 20261105000035_display_tokens.down.sql
-- Refuses while display tokens or their tickets exist.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM display_tokens)
       OR EXISTS (SELECT 1 FROM ws_tickets WHERE display_token_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM audit_logs WHERE action IN ('DISPLAY_TOKEN_CREATED', 'DISPLAY_TOKEN_REVOKED')) THEN
        RAISE EXCEPTION 'Display token history exists; cannot roll back 000035.';
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
        'APPROVAL_REQUESTED', 'APPROVAL_APPROVED', 'APPROVAL_REJECTED',
        'STAFF_PHONE_SET', 'CUSTOMER_PHONE_VERIFIED',
        'OUTLET_CASH_MOVEMENT',
        'ORDER_DISCOUNT',
        'STAFF_BADGE_ISSUED', 'STAFF_BADGE_REVOKED',
        'TABLE_MOVE', 'TABLE_MERGE', 'TABLE_SPLIT',
        'TABLE_TIMER_EXTEND'
    )
);

ALTER TABLE ws_tickets DROP CONSTRAINT IF EXISTS chk_ws_tickets_subject;
ALTER TABLE ws_tickets ADD CONSTRAINT chk_ws_tickets_subject CHECK (
    (user_id IS NOT NULL AND auth_session_id IS NOT NULL AND guest_session_id IS NULL)
    OR (user_id IS NULL AND auth_session_id IS NULL AND guest_session_id IS NOT NULL));
ALTER TABLE ws_tickets DROP CONSTRAINT IF EXISTS fk_ws_tickets_display;
ALTER TABLE ws_tickets DROP COLUMN IF EXISTS display_token_id;

DROP TABLE IF EXISTS display_tokens;
