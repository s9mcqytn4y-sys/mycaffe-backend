-- 20261101000018_outbox_relay_and_ws_tickets.up.sql
-- Iteration 5c (decisions N1, QR2; risk R2): per-store event sequence for ordered delivery and
-- replay, relay discovery, and single-use WebSocket tickets.

CREATE TABLE IF NOT EXISTS store_event_sequences (
    store_id UUID PRIMARY KEY REFERENCES stores(id) ON DELETE RESTRICT,
    last_seq BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT chk_store_event_sequences_seq CHECK (last_seq >= 0)
);
ALTER TABLE store_event_sequences ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_store_event_sequences_tenant_isolation ON store_event_sequences;
CREATE POLICY p_store_event_sequences_tenant_isolation ON store_event_sequences FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON store_event_sequences TO mycaffe_app;

-- Existing events get sequence numbers in creation order and count as already published, so the
-- first relay run does not replay history to screens.
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS store_seq BIGINT;
WITH numbered AS (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY store_id ORDER BY created_at, id) AS seq
    FROM outbox_events
)
UPDATE outbox_events e SET store_seq = numbered.seq, processed_at = COALESCE(e.processed_at, NOW())
FROM numbered WHERE numbered.id = e.id;
INSERT INTO store_event_sequences (store_id, last_seq)
SELECT store_id, MAX(store_seq) FROM outbox_events GROUP BY store_id
ON CONFLICT (store_id) DO UPDATE SET last_seq = EXCLUDED.last_seq;
ALTER TABLE outbox_events ALTER COLUMN store_seq SET NOT NULL;
ALTER TABLE outbox_events DROP CONSTRAINT IF EXISTS uq_outbox_events_store_seq;
ALTER TABLE outbox_events ADD CONSTRAINT uq_outbox_events_store_seq UNIQUE (store_id, store_seq);
CREATE INDEX IF NOT EXISTS idx_outbox_events_pending_seq
    ON outbox_events(store_id, store_seq) WHERE processed_at IS NULL;

-- Runs as the inserting role inside its tenant transaction. The sequence row stays locked until
-- commit, so a store's events commit in sequence order (one writer at a time per store).
CREATE OR REPLACE FUNCTION assign_outbox_store_seq()
RETURNS TRIGGER
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
BEGIN
    INSERT INTO store_event_sequences (store_id, last_seq) VALUES (NEW.store_id, 1)
    ON CONFLICT (store_id) DO UPDATE SET last_seq = store_event_sequences.last_seq + 1
    RETURNING last_seq INTO NEW.store_seq;
    PERFORM pg_notify('outbox', NEW.store_id::text);
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_outbox_events_store_seq ON outbox_events;
CREATE TRIGGER trg_outbox_events_store_seq
    BEFORE INSERT ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION assign_outbox_store_seq();

-- Relay discovery across tenants (decision QR2): store ids only.
CREATE OR REPLACE FUNCTION list_stores_with_pending_outbox(p_limit INT)
RETURNS TABLE (store_id UUID)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT DISTINCT e.store_id
    FROM outbox_events e
    WHERE e.processed_at IS NULL
    LIMIT GREATEST(1, LEAST(p_limit, 500));
$$;
REVOKE ALL ON FUNCTION list_stores_with_pending_outbox(INT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION list_stores_with_pending_outbox(INT) TO mycaffe_app;

-- Single-use WebSocket tickets (30 s). Exactly one of staff (user + auth session) or guest session.
CREATE TABLE IF NOT EXISTS ws_tickets (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id         UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    token_hash       BYTEA NOT NULL,
    user_id          UUID,
    auth_session_id  UUID,
    guest_session_id UUID,
    channels         TEXT[] NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL,
    used_at          TIMESTAMPTZ,
    CONSTRAINT uq_ws_tickets_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_ws_tickets_user FOREIGN KEY (store_id, user_id) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_ws_tickets_guest FOREIGN KEY (store_id, guest_session_id) REFERENCES guest_sessions(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_ws_tickets_hash CHECK (octet_length(token_hash) = 32),
    CONSTRAINT chk_ws_tickets_subject CHECK (
        (user_id IS NOT NULL AND auth_session_id IS NOT NULL AND guest_session_id IS NULL)
        OR (user_id IS NULL AND auth_session_id IS NULL AND guest_session_id IS NOT NULL)),
    CONSTRAINT chk_ws_tickets_channels CHECK (cardinality(channels) BETWEEN 1 AND 8),
    CONSTRAINT chk_ws_tickets_expiry CHECK (expires_at > created_at)
);
ALTER TABLE ws_tickets ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_ws_tickets_tenant_isolation ON ws_tickets;
CREATE POLICY p_ws_tickets_tenant_isolation ON ws_tickets FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON ws_tickets TO mycaffe_app;
