-- 20261101000018_outbox_relay_and_ws_tickets.down.sql
-- Tickets are short-lived (30 s) and carry no history, so they are dropped; the sequence column goes
-- with its trigger. Event rows themselves are kept.

DROP TABLE IF EXISTS ws_tickets;
DROP FUNCTION IF EXISTS list_stores_with_pending_outbox(INT);
DROP TRIGGER IF EXISTS trg_outbox_events_store_seq ON outbox_events;
DROP FUNCTION IF EXISTS assign_outbox_store_seq();
DROP INDEX IF EXISTS idx_outbox_events_pending_seq;
ALTER TABLE outbox_events DROP CONSTRAINT IF EXISTS uq_outbox_events_store_seq;
ALTER TABLE outbox_events DROP COLUMN IF EXISTS store_seq;
DROP TABLE IF EXISTS store_event_sequences;
