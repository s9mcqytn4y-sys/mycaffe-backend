-- 20261108000053_cassy_branding_and_mobile_profiles.up.sql
-- Cassy Mobile POS/KDS device profiles, tablet orientations, hardware timeouts, and mobile app flavor configs.

CREATE TABLE IF NOT EXISTS store_mobile_profiles (
    store_id                   UUID PRIMARY KEY REFERENCES stores(id) ON DELETE RESTRICT,
    app_brand_display_name     VARCHAR(100) NOT NULL DEFAULT 'Cassy',
    app_monogram_text          VARCHAR(10)  NOT NULL DEFAULT 'CS',
    app_technical_bundle_id    VARCHAR(120) NOT NULL DEFAULT 'com.azuredevid.cassy',
    pos_tablet_orientation     VARCHAR(25)  NOT NULL DEFAULT 'landscape_primary',
    pos_auto_lock_seconds      INT          NOT NULL DEFAULT 300,
    kds_auto_refresh_seconds   INT          NOT NULL DEFAULT 5,
    kds_sound_alert_enabled    BOOLEAN      NOT NULL DEFAULT true,
    cash_drawer_kick_on_cash   BOOLEAN      NOT NULL DEFAULT true,
    default_printer_interface  VARCHAR(20)  NOT NULL DEFAULT 'lan_tcp',
    default_printer_port       INT          NOT NULL DEFAULT 9100,
    offline_sync_interval_sec  INT          NOT NULL DEFAULT 60,
    updated_by_user_id         UUID,
    updated_at                 TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_mobile_profiles_user FOREIGN KEY (store_id, updated_by_user_id)
        REFERENCES users(store_id, id) ON DELETE SET NULL (updated_by_user_id),
    CONSTRAINT chk_mobile_tablet_orientation CHECK (
        pos_tablet_orientation IN ('landscape_primary', 'landscape_secondary', 'portrait_primary', 'auto_sensor')
    ),
    CONSTRAINT chk_mobile_auto_lock CHECK (pos_auto_lock_seconds BETWEEN 0 AND 3600),
    CONSTRAINT chk_kds_refresh_rate CHECK (kds_auto_refresh_seconds BETWEEN 2 AND 60),
    CONSTRAINT chk_mobile_printer_iface CHECK (default_printer_interface IN ('lan_tcp', 'bluetooth_ble', 'usb_otg')),
    CONSTRAINT chk_mobile_printer_port CHECK (default_printer_port BETWEEN 1 AND 65535),
    CONSTRAINT chk_mobile_sync_interval CHECK (offline_sync_interval_sec BETWEEN 15 AND 600)
);

CREATE TRIGGER trg_store_mobile_profiles_updated_at
BEFORE UPDATE ON store_mobile_profiles
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE store_mobile_profiles ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_store_mobile_profiles_isolation ON store_mobile_profiles;
CREATE POLICY p_store_mobile_profiles_isolation ON store_mobile_profiles FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

GRANT SELECT, INSERT, UPDATE ON store_mobile_profiles TO mycaffe_app;

ALTER TABLE governance_audit_logs
    DROP CONSTRAINT IF EXISTS chk_gov_audit_action;
ALTER TABLE governance_audit_logs
    ADD CONSTRAINT chk_gov_audit_action CHECK (action IN (
        'LEGAL_ACCEPTED', 'KYB_UPDATED', 'KYB_REVIEWED',
        'BYOK_MIDTRANS_UPDATED', 'BYOK_MIDTRANS_VERIFIED', 'BYOK_MIDTRANS_DISABLED',
        'STORE_CMS_UPDATED', 'NOTIFICATION_CONFIG_UPDATED',
        'NETWORK_NODE_UPSERTED', 'NETWORK_SIMULATION_RUN',
        'CLEARANCE_REQUESTED', 'CLEARANCE_CANCELLED', 'CLEARANCE_EXECUTED',
        'PLATFORM_INVOICE_CREATED', 'PLATFORM_INVOICE_SETTLED',
        'PLATFORM_INVOICE_WEBHOOK_SETTLED', 'PLATFORM_INVOICE_WEBHOOK_CLOSED',
        'MIKROTIK_PROFILE_UPDATED', 'MIKROTIK_RSC_GENERATED',
        'ONBOARDING_STORE_PROVISIONED', 'MOBILE_PROFILE_UPDATED'
    ));

