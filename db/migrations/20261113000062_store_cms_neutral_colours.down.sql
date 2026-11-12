UPDATE store_cms_configs SET primary_hex = '#2C1A12' WHERE primary_hex = '';
UPDATE store_cms_configs SET accent_hex = '#C68B59' WHERE accent_hex = '';

ALTER TABLE store_cms_configs
    DROP CONSTRAINT chk_store_cms_primary_hex,
    DROP CONSTRAINT chk_store_cms_accent_hex,
    ALTER COLUMN primary_hex TYPE CHAR(7),
    ALTER COLUMN accent_hex TYPE CHAR(7),
    ALTER COLUMN primary_hex SET DEFAULT '#2C1A12',
    ALTER COLUMN accent_hex SET DEFAULT '#C68B59',
    ADD CONSTRAINT chk_store_cms_primary_hex CHECK (primary_hex ~ '^#[0-9A-Fa-f]{6}$'),
    ADD CONSTRAINT chk_store_cms_accent_hex CHECK (accent_hex ~ '^#[0-9A-Fa-f]{6}$');
