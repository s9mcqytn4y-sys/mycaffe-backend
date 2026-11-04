-- 20261104000026_messaging_otp_customers.up.sql
-- v0.8 PR 8.2 (decisions V4, V5): SMS/WhatsApp delivery queue, one-time codes for staff PIN reset and
-- customer phone verification, verified customers, and staff phone numbers. Phones are E.164 (+62...).

ALTER TABLE users ADD COLUMN IF NOT EXISTS phone VARCHAR(16);
ALTER TABLE users DROP CONSTRAINT IF EXISTS chk_users_phone;
ALTER TABLE users ADD CONSTRAINT chk_users_phone CHECK (phone IS NULL OR phone ~ '^\+[1-9][0-9]{7,14}$');
CREATE UNIQUE INDEX IF NOT EXISTS uq_users_store_phone ON users(store_id, phone) WHERE phone IS NOT NULL;

CREATE TABLE IF NOT EXISTS customers (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id          UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    phone             VARCHAR(16) NOT NULL,
    name              VARCHAR(100),
    phone_verified_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_customers_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_customers_store_phone UNIQUE (store_id, phone),
    CONSTRAINT chk_customers_phone CHECK (phone ~ '^\+[1-9][0-9]{7,14}$')
);

CREATE TABLE IF NOT EXISTS otp_challenges (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id     UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    purpose      VARCHAR(20) NOT NULL,
    phone        VARCHAR(16) NOT NULL,
    user_id      UUID,
    customer_id  UUID,
    code_hash    CHAR(64) NOT NULL,
    attempts     SMALLINT NOT NULL DEFAULT 0,
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_otp_challenges_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_otp_challenges_user FOREIGN KEY (store_id, user_id) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_otp_challenges_customer FOREIGN KEY (store_id, customer_id) REFERENCES customers(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_otp_challenges_purpose CHECK (
        (purpose = 'staff_pin_reset' AND user_id IS NOT NULL AND customer_id IS NULL)
        OR (purpose = 'customer_phone' AND customer_id IS NOT NULL AND user_id IS NULL)),
    CONSTRAINT chk_otp_challenges_attempts CHECK (attempts BETWEEN 0 AND 10),
    CONSTRAINT chk_otp_challenges_expiry CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS idx_otp_challenges_phone ON otp_challenges(store_id, phone, created_at);
CREATE INDEX IF NOT EXISTS idx_otp_challenges_expires_at ON otp_challenges(expires_at);

CREATE TABLE IF NOT EXISTS message_outbox (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id        UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    channel         VARCHAR(10) NOT NULL,
    to_phone        VARCHAR(16) NOT NULL,
    template        VARCHAR(50) NOT NULL,
    body            TEXT NOT NULL,
    reference_id    VARCHAR(100) NOT NULL DEFAULT '',
    status          VARCHAR(10) NOT NULL DEFAULT 'pending',
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_until    TIMESTAMPTZ,
    last_error      VARCHAR(500) NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at         TIMESTAMPTZ,
    CONSTRAINT uq_message_outbox_store_id UNIQUE (store_id, id),
    CONSTRAINT chk_message_outbox_channel CHECK (channel IN ('sms', 'whatsapp')),
    CONSTRAINT chk_message_outbox_status CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    CONSTRAINT chk_message_outbox_phone CHECK (to_phone ~ '^\+[1-9][0-9]{7,14}$')
);
CREATE INDEX IF NOT EXISTS idx_message_outbox_due ON message_outbox(store_id, next_attempt_at) WHERE status IN ('pending', 'sending');
CREATE INDEX IF NOT EXISTS idx_message_outbox_created_at ON message_outbox(created_at);

ALTER TABLE customers ENABLE ROW LEVEL SECURITY;
ALTER TABLE otp_challenges ENABLE ROW LEVEL SECURITY;
ALTER TABLE message_outbox ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_customers_tenant_isolation ON customers;
CREATE POLICY p_customers_tenant_isolation ON customers FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
DROP POLICY IF EXISTS p_otp_challenges_tenant_isolation ON otp_challenges;
CREATE POLICY p_otp_challenges_tenant_isolation ON otp_challenges FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
DROP POLICY IF EXISTS p_message_outbox_tenant_isolation ON message_outbox;
CREATE POLICY p_message_outbox_tenant_isolation ON message_outbox FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON customers, otp_challenges, message_outbox TO mycaffe_app;

-- Worker tenant discovery, same shape as list_stores_with_due_email: store ids only.
CREATE OR REPLACE FUNCTION list_stores_with_due_message()
RETURNS TABLE (store_id UUID)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT DISTINCT m.store_id
    FROM message_outbox m
    WHERE (m.status = 'pending' AND m.next_attempt_at <= NOW())
       OR (m.status = 'sending' AND m.locked_until IS NOT NULL AND m.locked_until <= NOW())
$$;
REVOKE ALL ON FUNCTION list_stores_with_due_message() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION list_stores_with_due_message() TO mycaffe_app;

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
        'STAFF_PHONE_SET', 'CUSTOMER_PHONE_VERIFIED'
    )
);
