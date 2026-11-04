-- 20261104000025_outbox_dead_letter_and_purge.up.sql
-- P0-08: an outbox event that keeps failing to relay is dead-lettered instead of blocking its store,
-- and the operator purge job (migrate -action=purge) gets indexes for its retention scans.

ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS dead_at TIMESTAMPTZ;
ALTER TABLE outbox_events DROP CONSTRAINT IF EXISTS chk_outbox_events_dead;
ALTER TABLE outbox_events ADD CONSTRAINT chk_outbox_events_dead CHECK (dead_at IS NULL OR processed_at IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_outbox_events_processed_at ON outbox_events(processed_at) WHERE processed_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_outbox_events_dead ON outbox_events(store_id, dead_at) WHERE dead_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ws_tickets_expires_at ON ws_tickets(expires_at);
CREATE INDEX IF NOT EXISTS idx_guest_sessions_expires_at ON guest_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_receipt_tokens_expires_at ON receipt_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_account_tokens_expires_at ON account_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires_at ON auth_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_email_outbox_sent_at ON email_outbox(sent_at) WHERE sent_at IS NOT NULL;
