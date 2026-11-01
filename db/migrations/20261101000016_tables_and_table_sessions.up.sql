-- 20261101000016_tables_and_table_sessions.up.sql
-- Iteration 5a (decisions T1, QR1–QR3, QR7): tables with an opaque rotatable QR token, table sessions
-- whose state drives occupancy, and the pre-tenant QR resolver.

CREATE TABLE IF NOT EXISTS tables (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id      UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    table_number  VARCHAR(50)  NOT NULL,
    label         VARCHAR(100) NOT NULL DEFAULT '',
    section       VARCHAR(50)  NOT NULL DEFAULT '',
    capacity      INT          NOT NULL DEFAULT 4,
    qr_token      VARCHAR(64)  NOT NULL,
    qr_rotated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    is_active     BOOLEAN      NOT NULL DEFAULT true,
    archived_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_tables_store_id UNIQUE (store_id, id),
    CONSTRAINT chk_tables_capacity CHECK (capacity BETWEEN 1 AND 100),
    CONSTRAINT chk_tables_number CHECK (length(btrim(table_number)) > 0),
    CONSTRAINT chk_tables_qr_token CHECK (length(qr_token) >= 22)
);
-- The resolver looks the token up before a store is known, so it is unique across stores.
CREATE UNIQUE INDEX IF NOT EXISTS uq_tables_qr_token ON tables(qr_token);
CREATE UNIQUE INDEX IF NOT EXISTS uq_tables_store_number_active
    ON tables(store_id, lower(table_number)) WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS table_sessions (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id          UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    table_id          UUID NOT NULL,
    status            VARCHAR(10) NOT NULL DEFAULT 'open',
    opened_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    opened_via        VARCHAR(10) NOT NULL,
    opened_by_user_id UUID,
    settled_at        TIMESTAMPTZ,
    closed_at         TIMESTAMPTZ,
    closed_by_user_id UUID,
    close_reason      VARCHAR(20),
    CONSTRAINT uq_table_sessions_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_table_sessions_table FOREIGN KEY (store_id, table_id) REFERENCES tables(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_table_sessions_opened_by FOREIGN KEY (store_id, opened_by_user_id)
        REFERENCES users(store_id, id) ON DELETE SET NULL (opened_by_user_id),
    CONSTRAINT fk_table_sessions_closed_by FOREIGN KEY (store_id, closed_by_user_id)
        REFERENCES users(store_id, id) ON DELETE SET NULL (closed_by_user_id),
    CONSTRAINT chk_table_sessions_status CHECK (status IN ('open', 'closed')),
    CONSTRAINT chk_table_sessions_via CHECK (opened_via IN ('qr', 'staff')),
    CONSTRAINT chk_table_sessions_closed CHECK ((status = 'closed') = (closed_at IS NOT NULL)),
    CONSTRAINT chk_table_sessions_reason CHECK (close_reason IS NULL OR close_reason IN ('cashier', 'auto'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_table_sessions_one_open
    ON table_sessions(store_id, table_id) WHERE status = 'open';

-- orders.table_id exists since 000011 without a foreign key; queue_number and table_number exist too.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM orders WHERE table_id IS NOT NULL) THEN
        RAISE EXCEPTION 'orders.table_id has values but no tables exist yet; clear them before migrating.';
    END IF;
END $$;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS table_session_id UUID;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_table;
ALTER TABLE orders ADD CONSTRAINT fk_orders_table
    FOREIGN KEY (store_id, table_id) REFERENCES tables(store_id, id) ON DELETE RESTRICT;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_table_session;
ALTER TABLE orders ADD CONSTRAINT fk_orders_table_session
    FOREIGN KEY (store_id, table_session_id) REFERENCES table_sessions(store_id, id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_orders_table_session ON orders(store_id, table_session_id);

ALTER TABLE tables ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_tables_tenant_isolation ON tables;
CREATE POLICY p_tables_tenant_isolation ON tables FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE table_sessions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_table_sessions_tenant_isolation ON table_sessions;
CREATE POLICY p_table_sessions_tenant_isolation ON table_sessions FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
-- No DELETE: tables are archived and sessions closed, order history keeps referencing both.
GRANT SELECT, INSERT, UPDATE ON tables, table_sessions TO mycaffe_app;

-- Pre-tenant QR resolution (decision QR2). Returns only (store_id, table_id, is_active); everything
-- else is read inside the tenant transaction.
CREATE OR REPLACE FUNCTION resolve_table_qr(p_token VARCHAR)
RETURNS TABLE (store_id UUID, table_id UUID, is_active BOOLEAN)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT t.store_id, t.id, (t.is_active AND t.archived_at IS NULL AND s.is_active)
    FROM tables t
    JOIN stores s ON s.id = t.store_id AND s.deleted_at IS NULL
    WHERE t.qr_token = p_token
    LIMIT 1;
$$;
REVOKE ALL ON FUNCTION resolve_table_qr(VARCHAR) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_table_qr(VARCHAR) TO mycaffe_app;

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
        'TABLE_CREATE', 'TABLE_UPDATE', 'TABLE_QR_ROTATE', 'TABLE_SESSION_CLOSE'
    )
);
