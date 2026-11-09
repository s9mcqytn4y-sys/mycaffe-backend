-- 20261110000057_guest_split_shares.down.sql
-- Refuses while links exist: guests may still be paying through them.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM guest_split_shares) THEN
        RAISE EXCEPTION 'Guest split links exist; cannot roll back 000057.';
    END IF;
END $$;

DROP FUNCTION IF EXISTS resolve_split_share(BYTEA);
DROP TABLE IF EXISTS guest_split_shares;
