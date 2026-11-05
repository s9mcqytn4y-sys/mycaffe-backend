-- 20261105000033_table_session_links.up.sql
-- v0.9 PR 9.1 (decision W5): move, merge and split tables. Merging links a table to the primary
-- session of another table; its own session closes with reason 'merged' and its orders and guest
-- sessions move to the primary session. A table has at most one active link. Splitting unlinks.

CREATE TABLE IF NOT EXISTS table_session_links (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id           UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    primary_session_id UUID NOT NULL,
    table_id           UUID NOT NULL,
    linked_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    linked_by_user_id  UUID,
    unlinked_at        TIMESTAMPTZ,
    unlinked_by_user_id UUID,
    CONSTRAINT uq_table_session_links_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_table_session_links_session FOREIGN KEY (store_id, primary_session_id)
        REFERENCES table_sessions(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_table_session_links_table FOREIGN KEY (store_id, table_id)
        REFERENCES tables(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_table_session_links_linked_by FOREIGN KEY (store_id, linked_by_user_id)
        REFERENCES users(store_id, id) ON DELETE SET NULL (linked_by_user_id),
    CONSTRAINT fk_table_session_links_unlinked_by FOREIGN KEY (store_id, unlinked_by_user_id)
        REFERENCES users(store_id, id) ON DELETE SET NULL (unlinked_by_user_id),
    CONSTRAINT chk_table_session_links_unlinked CHECK (unlinked_at IS NULL OR unlinked_at >= linked_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_table_session_links_active
    ON table_session_links(store_id, table_id) WHERE unlinked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_table_session_links_primary
    ON table_session_links(store_id, primary_session_id) WHERE unlinked_at IS NULL;

ALTER TABLE table_session_links ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_table_session_links_tenant_isolation ON table_session_links;
CREATE POLICY p_table_session_links_tenant_isolation ON table_session_links FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON table_session_links TO mycaffe_app;

-- A guest device remembers the table it scanned, so orders from a merged table keep that table
-- (kitchen delivery) while billing on the shared session. NULL for sessions created before 000033.
ALTER TABLE guest_sessions ADD COLUMN IF NOT EXISTS table_id UUID;
ALTER TABLE guest_sessions DROP CONSTRAINT IF EXISTS fk_guest_sessions_table;
ALTER TABLE guest_sessions ADD CONSTRAINT fk_guest_sessions_table
    FOREIGN KEY (store_id, table_id) REFERENCES tables(store_id, id) ON DELETE RESTRICT;

ALTER TABLE table_sessions DROP CONSTRAINT IF EXISTS chk_table_sessions_reason;
ALTER TABLE table_sessions ADD CONSTRAINT chk_table_sessions_reason
    CHECK (close_reason IS NULL OR close_reason IN ('cashier', 'auto', 'merged'));

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
        'TABLE_MOVE', 'TABLE_MERGE', 'TABLE_SPLIT'
    )
);
