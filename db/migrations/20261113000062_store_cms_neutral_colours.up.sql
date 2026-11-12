-- PRD v4 Q9: an outlet without its own colour stores none, so the guest app and POS use their neutral fallback
-- instead of the old espresso. Rows that already hold a colour keep it.
ALTER TABLE store_cms_configs
    DROP CONSTRAINT chk_store_cms_primary_hex,
    DROP CONSTRAINT chk_store_cms_accent_hex,
    ALTER COLUMN primary_hex TYPE VARCHAR(7),
    ALTER COLUMN accent_hex TYPE VARCHAR(7),
    ALTER COLUMN primary_hex SET DEFAULT '',
    ALTER COLUMN accent_hex SET DEFAULT '',
    ADD CONSTRAINT chk_store_cms_primary_hex CHECK (primary_hex = '' OR primary_hex ~ '^#[0-9A-Fa-f]{6}$'),
    ADD CONSTRAINT chk_store_cms_accent_hex CHECK (accent_hex = '' OR accent_hex ~ '^#[0-9A-Fa-f]{6}$');
