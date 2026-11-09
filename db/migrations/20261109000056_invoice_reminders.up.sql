-- 20261109000056_invoice_reminders.up.sql
-- v0.11.1 PR 11.10 (X18): automatic reminders at D-3, the due date and D+7. One ledger row per invoice and
-- stage makes each reminder go out once; it is written in the same transaction as the outbox rows.

CREATE TABLE IF NOT EXISTS invoice_reminders (
    store_id   UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    invoice_id UUID NOT NULL,
    stage      VARCHAR(10) NOT NULL,
    channels   VARCHAR(20) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (store_id, invoice_id, stage),
    CONSTRAINT fk_invoice_reminders_invoice FOREIGN KEY (store_id, invoice_id) REFERENCES invoices(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_invoice_reminders_stage CHECK (stage IN ('d_minus_3', 'due', 'd_plus_7'))
);

ALTER TABLE invoice_reminders ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_invoice_reminders_tenant_isolation ON invoice_reminders;
CREATE POLICY p_invoice_reminders_tenant_isolation ON invoice_reminders FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT ON invoice_reminders TO mycaffe_app;

-- Worker tenant discovery, same shape as list_stores_with_due_message: store ids only.
CREATE OR REPLACE FUNCTION list_stores_with_open_invoices()
RETURNS TABLE (store_id UUID)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT DISTINCT i.store_id FROM invoices i WHERE i.paid_amount < i.total_amount
$$;
REVOKE ALL ON FUNCTION list_stores_with_open_invoices() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION list_stores_with_open_invoices() TO mycaffe_app;
