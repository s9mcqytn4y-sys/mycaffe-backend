-- 20261104000025_outbox_dead_letter_and_purge.down.sql
-- Dead-lettered events become ordinary processed events again; only indexes and the marker go.

DROP INDEX IF EXISTS idx_email_outbox_sent_at;
DROP INDEX IF EXISTS idx_auth_sessions_expires_at;
DROP INDEX IF EXISTS idx_account_tokens_expires_at;
DROP INDEX IF EXISTS idx_receipt_tokens_expires_at;
DROP INDEX IF EXISTS idx_guest_sessions_expires_at;
DROP INDEX IF EXISTS idx_ws_tickets_expires_at;
DROP INDEX IF EXISTS idx_outbox_events_dead;
DROP INDEX IF EXISTS idx_outbox_events_processed_at;
ALTER TABLE outbox_events DROP CONSTRAINT IF EXISTS chk_outbox_events_dead;
ALTER TABLE outbox_events DROP COLUMN IF EXISTS dead_at;
