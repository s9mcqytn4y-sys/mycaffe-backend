-- 20261109000056_invoice_reminders.down.sql
-- Refuses while reminders were sent: dropping the ledger would send them again.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM invoice_reminders) THEN
        RAISE EXCEPTION 'Invoice reminders exist; cannot roll back 000056.';
    END IF;
END $$;

DROP FUNCTION IF EXISTS list_stores_with_open_invoices();
DROP TABLE IF EXISTS invoice_reminders;
