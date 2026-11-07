-- 20261107000052_onboarding_otp_mikrotik_webhook.up.sql
-- Strict Onboarding 6-Digit Email OTP Verification, Superadmin Unverified Store Provisioning,
-- Tenant MikroTik RouterOS v7 (.rsc) & Winbox Network Profile (RLS), and Lane 1 Platform Invoice Webhook Resolver.

-- ============================================================================
-- 1. STRICT ONBOARDING EMAIL OTP COLUMNS & CONSTRAINTS
-- ============================================================================

ALTER TABLE tenant_onboarding_applications
    ADD COLUMN IF NOT EXISTS email_otp_hash       CHAR(64),
    ADD COLUMN IF NOT EXISTS email_otp_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS email_otp_sent_at    TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS email_otp_attempts   INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS email_verified_at    TIMESTAMPTZ;

ALTER TABLE tenant_onboarding_applications
    DROP CONSTRAINT IF EXISTS chk_onboarding_status;
ALTER TABLE tenant_onboarding_applications
    ADD CONSTRAINT chk_onboarding_status
    CHECK (status IN ('pending_email_otp', 'submitted', 'under_review', 'approved', 'rejected'));

ALTER TABLE tenant_onboarding_applications
    DROP CONSTRAINT IF EXISTS chk_onboarding_otp_hash;
ALTER TABLE tenant_onboarding_applications
    ADD CONSTRAINT chk_onboarding_otp_hash
    CHECK (email_otp_hash IS NULL OR email_otp_hash ~ '^[0-9a-f]{64}$');

ALTER TABLE tenant_onboarding_applications
    DROP CONSTRAINT IF EXISTS chk_onboarding_otp_attempts;
ALTER TABLE tenant_onboarding_applications
    ADD CONSTRAINT chk_onboarding_otp_attempts
    CHECK (email_otp_attempts >= 0);

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
        'MIKROTIK_PROFILE_UPDATED', 'MIKROTIK_RSC_GENERATED'
    ));

-- Allow unverified/trialing tenants to configure Midtrans Sandbox BYOK credentials immediately
-- upon provisioning (Production BYOK remains strictly gated on kyb_status = 'verified' in service layer).
UPDATE subscription_plans
SET supports_byok_midtrans = true, updated_at = NOW()
WHERE code IN ('trial_14d', 'starter_umkm');

-- ============================================================================
-- 2. TENANT MIKROTIK ROUTEROS V7 & WINBOX NETWORK AUTOMATION PROFILE (RLS)
-- ============================================================================

CREATE TABLE IF NOT EXISTS store_mikrotik_profiles (
    store_id               UUID PRIMARY KEY REFERENCES stores(id) ON DELETE RESTRICT,
    router_model           VARCHAR(100) NOT NULL DEFAULT 'hAP ax2 / RB750Gr3 (RouterOS v7)',
    wan_primary_interface  VARCHAR(64)  NOT NULL DEFAULT 'ether1-isp-utama',
    wan_backup_interface   VARCHAR(64)  NOT NULL DEFAULT 'ether2-modem-4g',
    pos_vlan_cidr          VARCHAR(32)  NOT NULL DEFAULT '192.168.10.0/24',
    staff_vlan_cidr        VARCHAR(32)  NOT NULL DEFAULT '192.168.20.0/24',
    guest_vlan_cidr        VARCHAR(32)  NOT NULL DEFAULT '192.168.30.0/23',
    pos_wifi_ssid          VARCHAR(64)  NOT NULL DEFAULT 'MyCaffe-POS-Internal',
    guest_wifi_ssid        VARCHAR(64)  NOT NULL DEFAULT 'MyCaffe-Guest-QR',
    guest_rate_limit_mbps  INT          NOT NULL DEFAULT 20,
    pos_reserved_mbps      INT          NOT NULL DEFAULT 10,
    sftp_port              INT          NOT NULL DEFAULT 2222,
    winbox_port            INT          NOT NULL DEFAULT 8291,
    winbox_allowed_cidr    VARCHAR(32)  NOT NULL DEFAULT '192.168.10.0/24',
    dns_servers            VARCHAR(120) NOT NULL DEFAULT '1.1.1.1,8.8.8.8',
    generated_rsc_sha256   VARCHAR(64)  NOT NULL DEFAULT '',
    updated_by_user_id     UUID,
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_mikrotik_updated_by FOREIGN KEY (store_id, updated_by_user_id)
        REFERENCES users(store_id, id) ON DELETE SET NULL (updated_by_user_id),
    CONSTRAINT chk_mikrotik_guest_rate CHECK (guest_rate_limit_mbps BETWEEN 1 AND 10000),
    CONSTRAINT chk_mikrotik_pos_rate CHECK (pos_reserved_mbps BETWEEN 1 AND 10000),
    -- Security rule: plain FTP port 21 is strictly forbidden; SFTP must run on an explicit SSH port >= 1024 (or 22).
    CONSTRAINT chk_mikrotik_sftp_no_plain_ftp CHECK ((sftp_port = 22 OR sftp_port BETWEEN 1024 AND 65535) AND sftp_port <> 21),
    CONSTRAINT chk_mikrotik_winbox_port CHECK (winbox_port BETWEEN 1024 AND 65535),
    CONSTRAINT chk_mikrotik_distinct_vlans CHECK (
        pos_vlan_cidr <> staff_vlan_cidr AND pos_vlan_cidr <> guest_vlan_cidr AND staff_vlan_cidr <> guest_vlan_cidr
    )
);

ALTER TABLE store_mikrotik_profiles ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_store_mikrotik_profiles_isolation ON store_mikrotik_profiles;
CREATE POLICY p_store_mikrotik_profiles_isolation ON store_mikrotik_profiles FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON store_mikrotik_profiles TO mycaffe_app;

-- ============================================================================
-- 3. NARROW SECURITY DEFINER FUNCTIONS (OWNED BY mycaffe_migrator)
-- ============================================================================

-- 3A. Pre-tenant resolver for Lane 1 Platform Subscription Invoice webhooks after SHA-512 verification.
CREATE OR REPLACE FUNCTION resolve_platform_invoice_webhook_context(p_provider_order_id VARCHAR)
RETURNS TABLE (invoice_id UUID, store_id UUID, amount_idr BIGINT, status VARCHAR)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT i.id, i.store_id, i.amount_idr, i.status
    FROM platform_invoices i
    WHERE i.provider_order_id = p_provider_order_id
    LIMIT 1;
$$;
REVOKE ALL ON FUNCTION resolve_platform_invoice_webhook_context(VARCHAR) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_platform_invoice_webhook_context(VARCHAR) TO mycaffe_app;

-- 3B. Kernel-enforced store provisioning from an Email-OTP-verified onboarding application.
-- Ensures mycaffe_app still cannot arbitrarily INSERT into stores without a verified onboarding record.
CREATE OR REPLACE FUNCTION provision_store_from_onboarding(
    p_app_id       UUID,
    p_store_id     UUID,
    p_owner_id     UUID,
    p_pin_hash     TEXT,
    p_review_notes TEXT
)
RETURNS TABLE (
    out_store_id    UUID,
    out_store_slug  VARCHAR,
    out_owner_id    UUID,
    out_owner_email VARCHAR,
    already_existed BOOLEAN
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_app tenant_onboarding_applications%ROWTYPE;
    v_existing_owner_id UUID;
    v_period_end TIMESTAMPTZ;
BEGIN
    SELECT * INTO v_app
    FROM tenant_onboarding_applications
    WHERE id = p_app_id
    FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'ONBOARDING_NOT_FOUND' USING ERRCODE = 'P0002';
    END IF;

    IF v_app.provisioned_store_id IS NOT NULL THEN
        SELECT u.id INTO v_existing_owner_id
        FROM users u
        WHERE u.store_id = v_app.provisioned_store_id AND u.role = 'owner'
        ORDER BY u.created_at ASC
        LIMIT 1;

        RETURN QUERY
        SELECT v_app.provisioned_store_id, v_app.requested_slug, v_existing_owner_id, v_app.owner_email, true;
        RETURN;
    END IF;

    IF v_app.email_verified_at IS NULL THEN
        RAISE EXCEPTION 'EMAIL_OTP_UNVERIFIED' USING ERRCODE = 'P0001';
    END IF;

    IF EXISTS (SELECT 1 FROM stores s WHERE lower(s.slug) = lower(v_app.requested_slug)) THEN
        RAISE EXCEPTION 'STORE_SLUG_CONFLICT' USING ERRCODE = '23505';
    END IF;

    v_period_end := CASE v_app.billing_cycle
        WHEN 'annual' THEN NOW() + INTERVAL '365 days'
        WHEN 'monthly' THEN NOW() + INTERVAL '30 days'
        ELSE NOW() + INTERVAL '14 days'
    END;

    INSERT INTO stores (
        id, slug, name, address, phone, timezone, is_active,
        order_prefix, queue_prefix, default_billing_mode, shift_mode,
        enabled_payment_methods, tax_bps, service_bps, qris_mdr_bps, currency
    ) VALUES (
        p_store_id,
        lower(btrim(v_app.requested_slug)),
        btrim(v_app.business_name),
        btrim(v_app.city_region),
        btrim(v_app.owner_phone),
        'Asia/Jakarta',
        true,
        'ORD',
        'A',
        'pay_first',
        'single_auto',
        '["qris","bank_transfer","gopay","shopeepay","cash","static_qris","edc"]'::jsonb,
        1000,
        0,
        70,
        'IDR'
    );

    INSERT INTO users (id, store_id, email, full_name, role, phone, pin_hash, is_active, pin_set_at)
    VALUES (
        p_owner_id,
        p_store_id,
        lower(btrim(v_app.owner_email)),
        btrim(v_app.owner_name),
        'owner',
        btrim(v_app.owner_phone),
        p_pin_hash,
        true,
        NOW()
    );

    INSERT INTO store_settings (store_id, updated_by)
    VALUES (p_store_id, p_owner_id)
    ON CONFLICT (store_id) DO NOTHING;

    INSERT INTO store_kyb_profiles (
        store_id, entity_type, legal_business_name, nib_masked, npwp_masked,
        kyb_status, plan_code, billing_cycle, subscription_status, current_period_ends_at,
        review_notes, updated_at
    ) VALUES (
        p_store_id,
        v_app.business_entity_type,
        btrim(v_app.business_name),
        v_app.nib_masked,
        v_app.npwp_masked,
        'unverified',
        v_app.selected_plan_code,
        v_app.billing_cycle,
        'trialing',
        v_period_end,
        COALESCE(p_review_notes, ''),
        NOW()
    )
    ON CONFLICT (store_id) DO NOTHING;

    INSERT INTO platform_tenant_registry (
        store_id, slug, display_name, plan_code, subscription_status, kyb_status, updated_at
    ) VALUES (
        p_store_id,
        lower(btrim(v_app.requested_slug)),
        btrim(v_app.business_name),
        v_app.selected_plan_code,
        'trialing',
        'unverified',
        NOW()
    )
    ON CONFLICT (store_id) DO UPDATE SET
        plan_code = EXCLUDED.plan_code,
        subscription_status = EXCLUDED.subscription_status,
        kyb_status = EXCLUDED.kyb_status,
        updated_at = NOW();

    UPDATE tenant_onboarding_applications
    SET provisioned_store_id = p_store_id,
        status = 'approved',
        review_notes = COALESCE(p_review_notes, ''),
        reviewed_at = NOW(),
        updated_at = NOW()
    WHERE id = p_app_id;

    RETURN QUERY
    SELECT p_store_id, v_app.requested_slug, p_owner_id, v_app.owner_email, false;
END;
$$;
REVOKE ALL ON FUNCTION provision_store_from_onboarding(UUID, UUID, UUID, TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION provision_store_from_onboarding(UUID, UUID, UUID, TEXT, TEXT) TO mycaffe_app;
