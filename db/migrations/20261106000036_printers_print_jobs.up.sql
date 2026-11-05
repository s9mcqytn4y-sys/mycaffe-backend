-- 20261106000036_printers_print_jobs.up.sql
-- v0.9 PR 9.4 (decision W8): server-side print queue. Each printer has a station, a format (ESC/POS
-- receipt paper or TSPL labels) and its own agent token ("<store_id>.<printer_id>.<secret>", hash
-- only). Jobs hold bytes rendered on the server; a print agent on the cafe LAN claims and acknowledges
-- them. Retries and dead-letter follow the outbox (000025).

CREATE TABLE IF NOT EXISTS printers (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id         UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    name             VARCHAR(50) NOT NULL,
    station          VARCHAR(10) NOT NULL,
    format           VARCHAR(10) NOT NULL,
    paper_width_mm   INT NOT NULL,
    label_height_mm  INT,
    copies           INT NOT NULL DEFAULT 1,
    footer           VARCHAR(100) NOT NULL DEFAULT '',
    agent_token_hash CHAR(64) NOT NULL,
    is_active        BOOLEAN NOT NULL DEFAULT true,
    last_seen_at     TIMESTAMPTZ,
    created_by       UUID NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_printers_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_printers_creator FOREIGN KEY (store_id, created_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_printers_name CHECK (length(btrim(name)) > 0),
    CONSTRAINT chk_printers_station CHECK (station IN ('bar', 'kitchen', 'checker', 'label')),
    CONSTRAINT chk_printers_format CHECK (
        (format = 'escpos' AND station <> 'label' AND paper_width_mm IN (58, 80) AND label_height_mm IS NULL)
        OR (format = 'tspl' AND station = 'label' AND paper_width_mm BETWEEN 25 AND 110 AND label_height_mm BETWEEN 15 AND 100)),
    CONSTRAINT chk_printers_copies CHECK (copies BETWEEN 1 AND 5)
);

CREATE TABLE IF NOT EXISTS print_jobs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id        UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    printer_id      UUID NOT NULL,
    order_id        UUID NOT NULL,
    round_id        UUID,
    kind            VARCHAR(10) NOT NULL,
    payload         BYTEA NOT NULL,
    status          VARCHAR(10) NOT NULL DEFAULT 'pending',
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at      TIMESTAMPTZ,
    printed_at      TIMESTAMPTZ,
    last_error      VARCHAR(500),
    reprinted_by_user_id UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_print_jobs_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_print_jobs_printer FOREIGN KEY (store_id, printer_id) REFERENCES printers(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_print_jobs_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_print_jobs_reprint_user FOREIGN KEY (store_id, reprinted_by_user_id) REFERENCES users(store_id, id) ON DELETE SET NULL (reprinted_by_user_id),
    CONSTRAINT chk_print_jobs_kind CHECK (kind IN ('ticket', 'checker', 'label')),
    CONSTRAINT chk_print_jobs_status CHECK (status IN ('pending', 'claimed', 'printed', 'dead')),
    CONSTRAINT chk_print_jobs_attempts CHECK (attempts >= 0),
    CONSTRAINT chk_print_jobs_payload CHECK (octet_length(payload) BETWEEN 1 AND 262144),
    CONSTRAINT chk_print_jobs_claimed CHECK ((status = 'claimed') = (claimed_at IS NOT NULL)),
    CONSTRAINT chk_print_jobs_printed CHECK ((status = 'printed') = (printed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_print_jobs_due ON print_jobs(store_id, printer_id, next_attempt_at)
    WHERE status IN ('pending', 'claimed');
CREATE INDEX IF NOT EXISTS idx_print_jobs_order ON print_jobs(store_id, order_id);

ALTER TABLE printers ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_printers_tenant_isolation ON printers;
CREATE POLICY p_printers_tenant_isolation ON printers FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE print_jobs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_print_jobs_tenant_isolation ON print_jobs;
CREATE POLICY p_print_jobs_tenant_isolation ON print_jobs FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON printers, print_jobs TO mycaffe_app;

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
        'PRINTER_CREATED', 'PRINTER_UPDATED', 'PRINTER_TOKEN_ROTATED', 'PRINT_REPRINT'
    )
);
