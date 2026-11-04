-- 20261104000024_approval_requests.up.sql
-- ADR-007 remote owner approval: a cashier asks the owner's phone to approve one void, refund, stock
-- override or shift variance. Single use, bound to action + reference + server-computed amount.

CREATE TABLE IF NOT EXISTS approval_requests (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id      UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    action        VARCHAR(20) NOT NULL,
    reference_id  UUID NOT NULL,
    params        JSONB NOT NULL DEFAULT '{}'::jsonb,
    amount        BIGINT NOT NULL,
    reason        TEXT NOT NULL,
    requested_by  UUID NOT NULL,
    status        VARCHAR(12) NOT NULL DEFAULT 'pending',
    decided_by    UUID,
    decision_note TEXT,
    expires_at    TIMESTAMPTZ NOT NULL,
    decided_at    TIMESTAMPTZ,
    consumed_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_approval_requests_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_approval_requests_requester FOREIGN KEY (store_id, requested_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_approval_requests_decider FOREIGN KEY (store_id, decided_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_approval_requests_action CHECK (action IN ('void', 'refund', 'stock_override', 'shift_variance')),
    CONSTRAINT chk_approval_requests_status CHECK (status IN ('pending', 'approved', 'rejected', 'consumed')),
    CONSTRAINT chk_approval_requests_reason CHECK (length(btrim(reason)) > 0),
    CONSTRAINT chk_approval_requests_not_self CHECK (decided_by IS NULL OR decided_by <> requested_by),
    CONSTRAINT chk_approval_requests_decided CHECK ((status = 'pending') = (decided_by IS NULL AND decided_at IS NULL)),
    CONSTRAINT chk_approval_requests_consumed CHECK ((status = 'consumed') = (consumed_at IS NOT NULL)),
    CONSTRAINT chk_approval_requests_expiry CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS idx_approval_requests_status ON approval_requests(store_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_approval_requests_requester ON approval_requests(store_id, requested_by, status);

ALTER TABLE approval_requests ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_approval_requests_tenant_isolation ON approval_requests;
CREATE POLICY p_approval_requests_tenant_isolation ON approval_requests FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON approval_requests TO mycaffe_app;

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
