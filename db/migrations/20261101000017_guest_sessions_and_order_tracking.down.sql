-- 20261101000017_guest_sessions_and_order_tracking.down.sql
-- Refuses while orders reference guest sessions: order history is never detached.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM orders WHERE guest_session_id IS NOT NULL OR tracking_token_hash IS NOT NULL) THEN
        RAISE EXCEPTION 'Orders reference guest sessions or tracking tokens; cannot drop them.';
    END IF;
END $$;

DROP FUNCTION IF EXISTS track_order_by_token(BYTEA);
DROP INDEX IF EXISTS uq_orders_tracking_token_hash;
DROP INDEX IF EXISTS idx_orders_guest_session;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_tracking_token_hash;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_guest_session;
ALTER TABLE orders DROP COLUMN IF EXISTS tracking_token_hash;
ALTER TABLE orders DROP COLUMN IF EXISTS guest_session_id;
DROP TABLE IF EXISTS guest_sessions;
