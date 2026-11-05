-- 20261105000035_display_tokens.up.sql
-- v0.9 PR 9.3 (decision W7): queue TV screens authenticate with a per-store display token
-- ("<store_id>.<token_id>.<secret>", only the SHA-256 of the secret is stored), revocable by the owner.
-- A display sees queue numbers and order status only, over REST and the WebSocket channel "queue".

CREATE TABLE IF NOT EXISTS display_tokens (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id     UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    name         VARCHAR(50) NOT NULL,
    token_hash   CHAR(64) NOT NULL,
    created_by   UUID NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at   TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ,
    CONSTRAINT uq_display_tokens_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_display_tokens_creator FOREIGN KEY (store_id, created_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_display_tokens_name CHECK (length(btrim(name)) > 0)
);
CREATE INDEX IF NOT EXISTS idx_display_tokens_active ON display_tokens(store_id) WHERE revoked_at IS NULL;

ALTER TABLE display_tokens ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_display_tokens_tenant_isolation ON display_tokens;
CREATE POLICY p_display_tokens_tenant_isolation ON display_tokens FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON display_tokens TO mycaffe_app;

-- WebSocket tickets gain a third subject: exactly one of staff, guest session or display token.
ALTER TABLE ws_tickets ADD COLUMN IF NOT EXISTS display_token_id UUID;
ALTER TABLE ws_tickets DROP CONSTRAINT IF EXISTS fk_ws_tickets_display;
ALTER TABLE ws_tickets ADD CONSTRAINT fk_ws_tickets_display FOREIGN KEY (store_id, display_token_id)
    REFERENCES display_tokens(store_id, id) ON DELETE RESTRICT;
ALTER TABLE ws_tickets DROP CONSTRAINT IF EXISTS chk_ws_tickets_subject;
ALTER TABLE ws_tickets ADD CONSTRAINT chk_ws_tickets_subject CHECK (
    (user_id IS NOT NULL AND auth_session_id IS NOT NULL AND guest_session_id IS NULL AND display_token_id IS NULL)
    OR (user_id IS NULL AND auth_session_id IS NULL AND guest_session_id IS NOT NULL AND display_token_id IS NULL)
    OR (user_id IS NULL AND auth_session_id IS NULL AND guest_session_id IS NULL AND display_token_id IS NOT NULL));

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
        'DISPLAY_TOKEN_CREATED', 'DISPLAY_TOKEN_REVOKED'
    )
);
