-- 20261106000038_devices_pin_verifiers_catalog_versions.up.sql
-- v0.10 PR 10.1 (ADR-005 OF2/OF3, amendments W19/W20): registered POS devices with their own credential
-- ("<store_id>.<device_id>.<secret>", hash only), offline PIN verifiers issued per device x staff (the
-- verifier itself is never stored: only who got one and until when), and catalog snapshots stored when the
-- catalog is served to a device (version = SHA-256 of the pricing content, insert-once).

CREATE TABLE IF NOT EXISTS devices (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id        UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    name            VARCHAR(50) NOT NULL,
    prefix          VARCHAR(4) NOT NULL,
    credential_hash CHAR(64) NOT NULL,
    registered_by   UUID NOT NULL,
    revoked_at      TIMESTAMPTZ,
    revoked_by      UUID,
    last_seen_at    TIMESTAMPTZ,
    last_sync_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_devices_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_devices_store_prefix UNIQUE (store_id, prefix),
    CONSTRAINT fk_devices_registered_by FOREIGN KEY (store_id, registered_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_devices_revoked_by FOREIGN KEY (store_id, revoked_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_devices_name CHECK (length(btrim(name)) > 0),
    CONSTRAINT chk_devices_prefix CHECK (prefix ~ '^[A-Z][A-Z0-9]{0,3}$'),
    CONSTRAINT chk_devices_revoked CHECK ((revoked_at IS NULL) = (revoked_by IS NULL))
);

CREATE TABLE IF NOT EXISTS device_pin_verifiers (
    store_id   UUID NOT NULL,
    device_id  UUID NOT NULL,
    user_id    UUID NOT NULL,
    issued_at  TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (store_id, device_id, user_id),
    CONSTRAINT fk_device_pin_verifiers_device FOREIGN KEY (store_id, device_id) REFERENCES devices(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_device_pin_verifiers_user FOREIGN KEY (store_id, user_id) REFERENCES users(store_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_device_pin_verifiers_expiry CHECK (expires_at > issued_at)
);

CREATE TABLE IF NOT EXISTS catalog_versions (
    store_id        UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    version         CHAR(64) NOT NULL,
    snapshot        JSONB NOT NULL,
    first_served_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (store_id, version),
    CONSTRAINT chk_catalog_versions_version CHECK (version ~ '^[0-9a-f]{64}$')
);

ALTER TABLE devices ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_devices_tenant_isolation ON devices;
CREATE POLICY p_devices_tenant_isolation ON devices FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE device_pin_verifiers ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_device_pin_verifiers_tenant_isolation ON device_pin_verifiers;
CREATE POLICY p_device_pin_verifiers_tenant_isolation ON device_pin_verifiers FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE catalog_versions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_catalog_versions_tenant_isolation ON catalog_versions;
CREATE POLICY p_catalog_versions_tenant_isolation ON catalog_versions FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

GRANT SELECT, INSERT, UPDATE ON devices TO mycaffe_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON device_pin_verifiers TO mycaffe_app;
-- Snapshots are immutable: no UPDATE or DELETE for the runtime role.
GRANT SELECT, INSERT ON catalog_versions TO mycaffe_app;

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
        'RECEIPT_SENT',
        'DEVICE_REGISTERED', 'DEVICE_RENAMED', 'DEVICE_REVOKED', 'DEVICE_PIN_VERIFIER_ISSUED'
    )
);
