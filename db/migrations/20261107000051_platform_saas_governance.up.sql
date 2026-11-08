-- 20261107000051_platform_saas_governance.up.sql
-- Multi-Tenant SaaS Governance, Legal/UU PDP Consent, Dual-Lane Midtrans (Platform Billing vs Store BYOK AES-256-GCM),
-- Storefront CMS & i18n, BYO-Email/WhatsApp Notifications, Self-Maintain Hardware/LAN Topology Nodes,
-- and Tax-Safe Account Clearance (ADR-009).

-- ============================================================================
-- 1. GLOBAL PLATFORM REFERENCE TABLES (Managed by Platform Superadmin)
-- ============================================================================

CREATE TABLE IF NOT EXISTS legal_documents (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    doc_type         VARCHAR(40) NOT NULL,
    version          VARCHAR(20) NOT NULL,
    locale           VARCHAR(10) NOT NULL DEFAULT 'id-ID',
    title            VARCHAR(200) NOT NULL,
    content_markdown TEXT NOT NULL,
    content_sha256   CHAR(64) NOT NULL,
    is_active        BOOLEAN NOT NULL DEFAULT true,
    effective_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_legal_documents_type_ver_locale UNIQUE (doc_type, version, locale),
    CONSTRAINT chk_legal_documents_type CHECK (doc_type IN ('terms_of_service', 'privacy_policy_pdp', 'dpa_tenant', 'refund_policy')),
    CONSTRAINT chk_legal_documents_locale CHECK (locale IN ('id-ID', 'en-US')),
    CONSTRAINT chk_legal_documents_sha256 CHECK (content_sha256 ~ '^[0-9a-f]{64}$')
);

CREATE TABLE IF NOT EXISTS subscription_plans (
    code                       VARCHAR(40) PRIMARY KEY,
    name_id                    VARCHAR(100) NOT NULL,
    name_en                    VARCHAR(100) NOT NULL,
    monthly_price_idr          BIGINT NOT NULL,
    annual_price_idr           BIGINT NOT NULL,
    commission_bps             INT NOT NULL DEFAULT 0,
    max_tables                 INT NOT NULL DEFAULT 15,
    max_printers               INT NOT NULL DEFAULT 1,
    supports_byok_midtrans     BOOLEAN NOT NULL DEFAULT false,
    supports_kds_queue_tv      BOOLEAN NOT NULL DEFAULT false,
    supports_whitelabel        BOOLEAN NOT NULL DEFAULT false,
    supports_byo_notifications BOOLEAN NOT NULL DEFAULT false,
    is_active                  BOOLEAN NOT NULL DEFAULT true,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_subscription_plans_monthly CHECK (monthly_price_idr >= 0),
    CONSTRAINT chk_subscription_plans_annual CHECK (annual_price_idr >= 0),
    CONSTRAINT chk_subscription_plans_zero_commission CHECK (commission_bps = 0),
    CONSTRAINT chk_subscription_plans_limits CHECK (max_tables >= 0 AND max_printers >= 0)
);

CREATE TABLE IF NOT EXISTS tenant_onboarding_applications (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    idempotency_key      VARCHAR(100) NOT NULL UNIQUE,
    requested_slug       VARCHAR(64) NOT NULL,
    business_name        VARCHAR(150) NOT NULL,
    business_entity_type VARCHAR(30) NOT NULL DEFAULT 'perorangan',
    owner_name           VARCHAR(120) NOT NULL,
    owner_email          VARCHAR(190) NOT NULL,
    owner_phone          VARCHAR(40) NOT NULL,
    city_region          VARCHAR(120) NOT NULL DEFAULT '',
    selected_plan_code   VARCHAR(40) NOT NULL REFERENCES subscription_plans(code) ON DELETE RESTRICT,
    billing_cycle        VARCHAR(20) NOT NULL DEFAULT 'trial',
    nib_masked           VARCHAR(32) NOT NULL DEFAULT '',
    npwp_masked          VARCHAR(32) NOT NULL DEFAULT '',
    accepted_tos_sha256  CHAR(64) NOT NULL,
    accepted_pdp_sha256  CHAR(64) NOT NULL,
    status               VARCHAR(30) NOT NULL DEFAULT 'submitted',
    provisioned_store_id UUID REFERENCES stores(id) ON DELETE RESTRICT,
    review_notes         TEXT NOT NULL DEFAULT '',
    reviewed_at          TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_onboarding_entity_type CHECK (business_entity_type IN ('perorangan', 'cv', 'pt', 'pt_perorangan', 'koperasi')),
    CONSTRAINT chk_onboarding_billing_cycle CHECK (billing_cycle IN ('trial', 'monthly', 'annual')),
    CONSTRAINT chk_onboarding_status CHECK (status IN ('submitted', 'under_review', 'approved', 'rejected')),
    CONSTRAINT chk_onboarding_tos_hash CHECK (accepted_tos_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_onboarding_pdp_hash CHECK (accepted_pdp_sha256 ~ '^[0-9a-f]{64}$')
);

CREATE TABLE IF NOT EXISTS platform_tenant_registry (
    store_id              UUID PRIMARY KEY REFERENCES stores(id) ON DELETE CASCADE,
    slug                  VARCHAR(100) NOT NULL,
    display_name          VARCHAR(150) NOT NULL,
    plan_code             VARCHAR(40) NOT NULL DEFAULT 'trial_14d' REFERENCES subscription_plans(code) ON DELETE RESTRICT,
    subscription_status   VARCHAR(30) NOT NULL DEFAULT 'trialing',
    kyb_status            VARCHAR(30) NOT NULL DEFAULT 'unverified',
    byok_midtrans_enabled BOOLEAN NOT NULL DEFAULT false,
    active_nodes_count    INT NOT NULL DEFAULT 0,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_registry_sub_status CHECK (subscription_status IN ('trialing', 'active', 'past_due', 'suspended', 'cleared')),
    CONSTRAINT chk_registry_kyb_status CHECK (kyb_status IN ('unverified', 'pending_review', 'verified', 'rejected', 'suspended')),
    CONSTRAINT chk_registry_nodes_nonneg CHECK (active_nodes_count >= 0)
);

-- ============================================================================
-- 2. TENANT-OWNED GOVERNANCE TABLES (PostgreSQL 18 Row-Level Security)
-- ============================================================================

CREATE TABLE IF NOT EXISTS tenant_legal_acceptances (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id           UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    legal_document_id  UUID NOT NULL REFERENCES legal_documents(id) ON DELETE RESTRICT,
    accepted_by_user_id UUID NOT NULL,
    document_sha256    CHAR(64) NOT NULL,
    accepted_ip        VARCHAR(64) NOT NULL DEFAULT '',
    user_agent         VARCHAR(255) NOT NULL DEFAULT '',
    accepted_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_tenant_legal_acceptance UNIQUE (store_id, legal_document_id, accepted_by_user_id),
    CONSTRAINT fk_tenant_legal_user FOREIGN KEY (store_id, accepted_by_user_id) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_tenant_legal_sha256 CHECK (document_sha256 ~ '^[0-9a-f]{64}$')
);

CREATE TABLE IF NOT EXISTS store_kyb_profiles (
    store_id               UUID PRIMARY KEY REFERENCES stores(id) ON DELETE RESTRICT,
    entity_type            VARCHAR(30) NOT NULL DEFAULT 'perorangan',
    legal_business_name    VARCHAR(150) NOT NULL,
    nib_masked             VARCHAR(32) NOT NULL DEFAULT '',
    npwp_masked            VARCHAR(32) NOT NULL DEFAULT '',
    pse_registration_no    VARCHAR(64) NOT NULL DEFAULT '',
    kyb_status             VARCHAR(30) NOT NULL DEFAULT 'unverified',
    plan_code              VARCHAR(40) NOT NULL DEFAULT 'trial_14d' REFERENCES subscription_plans(code) ON DELETE RESTRICT,
    billing_cycle          VARCHAR(20) NOT NULL DEFAULT 'trial',
    subscription_status    VARCHAR(30) NOT NULL DEFAULT 'trialing',
    current_period_ends_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '14 days'),
    verified_at            TIMESTAMPTZ,
    review_notes           TEXT NOT NULL DEFAULT '',
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_store_kyb_entity_type CHECK (entity_type IN ('perorangan', 'cv', 'pt', 'pt_perorangan', 'koperasi')),
    CONSTRAINT chk_store_kyb_status CHECK (kyb_status IN ('unverified', 'pending_review', 'verified', 'rejected', 'suspended')),
    CONSTRAINT chk_store_kyb_cycle CHECK (billing_cycle IN ('trial', 'monthly', 'annual')),
    CONSTRAINT chk_store_kyb_sub_status CHECK (subscription_status IN ('trialing', 'active', 'past_due', 'suspended', 'cleared'))
);

CREATE TABLE IF NOT EXISTS platform_invoices (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id             UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    invoice_number       VARCHAR(50) NOT NULL UNIQUE,
    plan_code            VARCHAR(40) NOT NULL REFERENCES subscription_plans(code) ON DELETE RESTRICT,
    billing_cycle        VARCHAR(20) NOT NULL,
    amount_idr           BIGINT NOT NULL,
    status               VARCHAR(30) NOT NULL DEFAULT 'pending',
    provider_environment VARCHAR(20) NOT NULL DEFAULT 'sandbox',
    provider_order_id    VARCHAR(100) UNIQUE,
    provider_qr_string   TEXT NOT NULL DEFAULT '',
    provider_va_number   VARCHAR(64) NOT NULL DEFAULT '',
    paid_at              TIMESTAMPTZ,
    due_at               TIMESTAMPTZ NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_platform_invoices_cycle CHECK (billing_cycle IN ('monthly', 'annual')),
    CONSTRAINT chk_platform_invoices_amount CHECK (amount_idr > 0),
    CONSTRAINT chk_platform_invoices_status CHECK (status IN ('pending', 'paid', 'expired', 'cancelled', 'refunded')),
    CONSTRAINT chk_platform_invoices_env CHECK (provider_environment IN ('sandbox', 'production'))
);
CREATE INDEX IF NOT EXISTS idx_platform_invoices_store_created ON platform_invoices(store_id, created_at DESC);

CREATE TABLE IF NOT EXISTS tenant_clearance_requests (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id                UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    requested_by_user_id    UUID NOT NULL,
    reason                  TEXT NOT NULL,
    status                  VARCHAR(40) NOT NULL DEFAULT 'cooling_off',
    cooling_off_ends_at     TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '7 days'),
    ledger_retention_until  TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '10 years'),
    executed_at             TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_clearance_requester FOREIGN KEY (store_id, requested_by_user_id) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_clearance_reason CHECK (length(btrim(reason)) >= 5),
    CONSTRAINT chk_clearance_status CHECK (status IN ('cooling_off', 'cancelled', 'pii_anonymized_ledger_retained'))
);
CREATE INDEX IF NOT EXISTS idx_tenant_clearance_store_status ON tenant_clearance_requests(store_id, status);

CREATE TABLE IF NOT EXISTS store_payment_credentials (
    store_id                     UUID PRIMARY KEY REFERENCES stores(id) ON DELETE RESTRICT,
    provider                     VARCHAR(30) NOT NULL DEFAULT 'midtrans',
    environment                  VARCHAR(20) NOT NULL DEFAULT 'sandbox',
    merchant_id                  VARCHAR(64) NOT NULL,
    client_key                   VARCHAR(128) NOT NULL,
    server_key_ciphertext        BYTEA NOT NULL,
    server_key_masked            VARCHAR(32) NOT NULL,
    key_version                  SMALLINT NOT NULL DEFAULT 1,
    validation_status            VARCHAR(20) NOT NULL DEFAULT 'unverified',
    is_enabled                   BOOLEAN NOT NULL DEFAULT false,
    fallback_static_qris_enabled BOOLEAN NOT NULL DEFAULT true,
    last_verified_at             TIMESTAMPTZ,
    updated_by_user_id           UUID,
    updated_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_store_pay_cred_provider CHECK (provider IN ('midtrans')),
    CONSTRAINT chk_store_pay_cred_env CHECK (environment IN ('sandbox', 'production')),
    CONSTRAINT chk_store_pay_cred_key_ver CHECK (key_version >= 1),
    CONSTRAINT chk_store_pay_cred_val_status CHECK (validation_status IN ('unverified', 'valid', 'invalid')),
    CONSTRAINT chk_store_pay_cred_enabled_valid CHECK (NOT is_enabled OR validation_status = 'valid')
);

CREATE TABLE IF NOT EXISTS store_cms_configs (
    store_id               UUID PRIMARY KEY REFERENCES stores(id) ON DELETE RESTRICT,
    default_locale         VARCHAR(10) NOT NULL DEFAULT 'id-ID',
    supported_locales      JSONB NOT NULL DEFAULT '["id-ID","en-US"]'::jsonb,
    hero_title_id          VARCHAR(140) NOT NULL DEFAULT '',
    hero_title_en          VARCHAR(140) NOT NULL DEFAULT '',
    hero_subtitle_id       VARCHAR(280) NOT NULL DEFAULT '',
    hero_subtitle_en       VARCHAR(280) NOT NULL DEFAULT '',
    hero_banner_url        VARCHAR(500) NOT NULL DEFAULT '',
    announcement_banner_id VARCHAR(280) NOT NULL DEFAULT '',
    announcement_banner_en VARCHAR(280) NOT NULL DEFAULT '',
    primary_hex            CHAR(7) NOT NULL DEFAULT '#2C1A12',
    accent_hex             CHAR(7) NOT NULL DEFAULT '#C68B59',
    high_contrast_mode     BOOLEAN NOT NULL DEFAULT false,
    hide_powered_by_badge  BOOLEAN NOT NULL DEFAULT false,
    custom_domain          VARCHAR(190) NOT NULL DEFAULT '',
    operating_hours        JSONB NOT NULL DEFAULT '{}'::jsonb,
    public_refund_policy   TEXT NOT NULL DEFAULT '',
    public_contact_phone   VARCHAR(40) NOT NULL DEFAULT '',
    public_contact_email   VARCHAR(160) NOT NULL DEFAULT '',
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_store_cms_locale CHECK (default_locale IN ('id-ID', 'en-US')),
    CONSTRAINT chk_store_cms_locales_json CHECK (jsonb_typeof(supported_locales) = 'array'),
    CONSTRAINT chk_store_cms_primary_hex CHECK (primary_hex ~ '^#[0-9A-Fa-f]{6}$'),
    CONSTRAINT chk_store_cms_accent_hex CHECK (accent_hex ~ '^#[0-9A-Fa-f]{6}$'),
    CONSTRAINT chk_store_cms_hours_json CHECK (jsonb_typeof(operating_hours) = 'object')
);

CREATE TABLE IF NOT EXISTS store_notification_configs (
    store_id                   UUID PRIMARY KEY REFERENCES stores(id) ON DELETE RESTRICT,
    email_mode                 VARCHAR(30) NOT NULL DEFAULT 'platform_shared',
    smtp_host                  VARCHAR(190) NOT NULL DEFAULT '',
    smtp_port                  INT NOT NULL DEFAULT 587,
    smtp_tls_mode              VARCHAR(20) NOT NULL DEFAULT 'starttls',
    smtp_username              VARCHAR(190) NOT NULL DEFAULT '',
    smtp_password_ciphertext   BYTEA,
    smtp_password_masked       VARCHAR(32) NOT NULL DEFAULT '',
    smtp_from_email            VARCHAR(190) NOT NULL DEFAULT '',
    smtp_from_name             VARCHAR(120) NOT NULL DEFAULT '',
    whatsapp_mode              VARCHAR(30) NOT NULL DEFAULT 'platform_shared',
    whatsapp_endpoint_url      VARCHAR(300) NOT NULL DEFAULT '',
    whatsapp_phone_number_id   VARCHAR(64) NOT NULL DEFAULT '',
    whatsapp_token_ciphertext  BYTEA,
    whatsapp_token_masked      VARCHAR(32) NOT NULL DEFAULT '',
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_store_notif_email_mode CHECK (email_mode IN ('disabled', 'platform_shared', 'custom_smtp')),
    CONSTRAINT chk_store_notif_smtp_port CHECK (smtp_port BETWEEN 1 AND 65535),
    CONSTRAINT chk_store_notif_smtp_tls CHECK (smtp_tls_mode IN ('starttls', 'implicit')),
    CONSTRAINT chk_store_notif_wa_mode CHECK (whatsapp_mode IN ('disabled', 'platform_shared', 'custom_bridge', 'custom_cloud_api'))
);

CREATE TABLE IF NOT EXISTS store_network_nodes (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id             UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    node_name            VARCHAR(100) NOT NULL,
    node_type            VARCHAR(30) NOT NULL,
    connection_protocol  VARCHAR(30) NOT NULL,
    local_ip             VARCHAR(64) NOT NULL DEFAULT '',
    local_port           INT NOT NULL DEFAULT 0,
    mac_address          VARCHAR(32) NOT NULL DEFAULT '',
    paper_width_mm       SMALLINT NOT NULL DEFAULT 80,
    drawer_kick_enabled  BOOLEAN NOT NULL DEFAULT false,
    health_status        VARCHAR(20) NOT NULL DEFAULT 'unknown',
    last_latency_ms      INT NOT NULL DEFAULT 0,
    last_heartbeat_at    TIMESTAMPTZ,
    metadata             JSONB NOT NULL DEFAULT '{}'::jsonb,
    is_active            BOOLEAN NOT NULL DEFAULT true,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_store_network_nodes_name UNIQUE (store_id, node_name),
    CONSTRAINT chk_store_network_node_type CHECK (node_type IN ('local_edge_server', 'router_gateway', 'pos_terminal', 'escpos_printer', 'cash_drawer', 'barcode_scanner', 'kds_screen', 'queue_tv', 'customer_display', 'iot_buzzer')),
    CONSTRAINT chk_store_network_proto CHECK (connection_protocol IN ('lan_tcp_9100', 'usb_hid', 'usb_raw', 'bluetooth_spp', 'bluetooth_ble', 'http_webhook', 'ws_realtime', 'rj11_dk_pulse')),
    CONSTRAINT chk_store_network_port CHECK (local_port BETWEEN 0 AND 65535),
    CONSTRAINT chk_store_network_paper CHECK (paper_width_mm IN (0, 58, 80)),
    CONSTRAINT chk_store_network_health CHECK (health_status IN ('unknown', 'online', 'degraded', 'offline')),
    CONSTRAINT chk_store_network_latency CHECK (last_latency_ms >= 0),
    CONSTRAINT chk_store_network_meta CHECK (jsonb_typeof(metadata) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_store_network_nodes_store_active ON store_network_nodes(store_id, is_active);

CREATE TABLE IF NOT EXISTS governance_audit_logs (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id      UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    actor_user_id UUID,
    actor_type    VARCHAR(20) NOT NULL,
    action        VARCHAR(50) NOT NULL,
    reason        TEXT NOT NULL DEFAULT '',
    metadata      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_gov_audit_actor_type CHECK (actor_type IN ('tenant_owner', 'tenant_staff', 'platform_admin', 'system')),
    CONSTRAINT chk_gov_audit_action CHECK (action IN (
        'LEGAL_ACCEPTED', 'KYB_UPDATED', 'KYB_REVIEWED',
        'BYOK_MIDTRANS_UPDATED', 'BYOK_MIDTRANS_VERIFIED', 'BYOK_MIDTRANS_DISABLED',
        'STORE_CMS_UPDATED', 'NOTIFICATION_CONFIG_UPDATED',
        'NETWORK_NODE_UPSERTED', 'NETWORK_SIMULATION_RUN',
        'CLEARANCE_REQUESTED', 'CLEARANCE_CANCELLED', 'CLEARANCE_EXECUTED',
        'PLATFORM_INVOICE_CREATED', 'PLATFORM_INVOICE_SETTLED'
    )),
    CONSTRAINT chk_gov_audit_meta CHECK (jsonb_typeof(metadata) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_gov_audit_store_created ON governance_audit_logs(store_id, created_at DESC);

-- ============================================================================
-- 3. ROW-LEVEL SECURITY & LEAST-PRIVILEGE GRANTS FOR mycaffe_app
-- ============================================================================

GRANT SELECT, INSERT, UPDATE ON legal_documents TO mycaffe_app;
GRANT SELECT, INSERT, UPDATE ON subscription_plans TO mycaffe_app;
GRANT SELECT, INSERT, UPDATE ON tenant_onboarding_applications TO mycaffe_app;
GRANT SELECT, INSERT, UPDATE ON platform_tenant_registry TO mycaffe_app;

ALTER TABLE tenant_legal_acceptances ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_tenant_legal_acceptances_isolation ON tenant_legal_acceptances;
CREATE POLICY p_tenant_legal_acceptances_isolation ON tenant_legal_acceptances FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT ON tenant_legal_acceptances TO mycaffe_app;

ALTER TABLE store_kyb_profiles ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_store_kyb_profiles_isolation ON store_kyb_profiles;
CREATE POLICY p_store_kyb_profiles_isolation ON store_kyb_profiles FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON store_kyb_profiles TO mycaffe_app;

ALTER TABLE platform_invoices ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_platform_invoices_isolation ON platform_invoices;
CREATE POLICY p_platform_invoices_isolation ON platform_invoices FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON platform_invoices TO mycaffe_app;

ALTER TABLE tenant_clearance_requests ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_tenant_clearance_requests_isolation ON tenant_clearance_requests;
CREATE POLICY p_tenant_clearance_requests_isolation ON tenant_clearance_requests FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON tenant_clearance_requests TO mycaffe_app;

ALTER TABLE store_payment_credentials ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_store_payment_credentials_isolation ON store_payment_credentials;
CREATE POLICY p_store_payment_credentials_isolation ON store_payment_credentials FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON store_payment_credentials TO mycaffe_app;

ALTER TABLE store_cms_configs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_store_cms_configs_isolation ON store_cms_configs;
CREATE POLICY p_store_cms_configs_isolation ON store_cms_configs FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON store_cms_configs TO mycaffe_app;

ALTER TABLE store_notification_configs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_store_notification_configs_isolation ON store_notification_configs;
CREATE POLICY p_store_notification_configs_isolation ON store_notification_configs FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON store_notification_configs TO mycaffe_app;

ALTER TABLE store_network_nodes ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_store_network_nodes_isolation ON store_network_nodes;
CREATE POLICY p_store_network_nodes_isolation ON store_network_nodes FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON store_network_nodes TO mycaffe_app;

ALTER TABLE governance_audit_logs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_governance_audit_logs_isolation ON governance_audit_logs;
CREATE POLICY p_governance_audit_logs_isolation ON governance_audit_logs FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT ON governance_audit_logs TO mycaffe_app;

-- ============================================================================
-- 4. DEFAULT SUBSCRIPTION PLANS & OFFICIAL LEGAL DOCUMENTS (Idempotent Seed)
-- ============================================================================

INSERT INTO subscription_plans (
    code, name_id, name_en, monthly_price_idr, annual_price_idr, commission_bps,
    max_tables, max_printers, supports_byok_midtrans, supports_kds_queue_tv, supports_whitelabel, supports_byo_notifications
) VALUES
    ('trial_14d', 'Uji Coba Gratis 14 Hari', '14-Day Free Trial', 0, 0, 0, 15, 1, false, true, false, false),
    ('starter_umkm', 'Starter UMKM', 'Starter SME', 149000, 1430400, 0, 15, 1, false, false, false, false),
    ('pro_cafe', 'Pro Cafe & Resto', 'Pro Cafe & Resto', 299000, 2870400, 0, 0, 0, true, true, false, true),
    ('enterprise_whitelabel', 'Enterprise White-Label', 'Enterprise White-Label', 599000, 5750400, 0, 0, 0, true, true, true, true)
ON CONFLICT (code) DO UPDATE SET
    name_id = EXCLUDED.name_id,
    name_en = EXCLUDED.name_en,
    monthly_price_idr = EXCLUDED.monthly_price_idr,
    annual_price_idr = EXCLUDED.annual_price_idr,
    supports_byok_midtrans = EXCLUDED.supports_byok_midtrans,
    supports_kds_queue_tv = EXCLUDED.supports_kds_queue_tv,
    supports_whitelabel = EXCLUDED.supports_whitelabel,
    supports_byo_notifications = EXCLUDED.supports_byo_notifications,
    updated_at = NOW();

INSERT INTO legal_documents (doc_type, version, locale, title, content_markdown, content_sha256)
VALUES
    (
        'terms_of_service', '1.0.0', 'id-ID',
        'Syarat dan Ketentuan Layanan SaaS MyCaffe by AzureDevID',
        'AzureDevID bertindak murni sebagai penyedia perangkat lunak (SaaS Provider) dengan skema Bring Your Own Key (BYOK) dan 0% komisi per transaksi. Dana transaksi menu mengalir langsung ke rekening masing-masing Tenant.',
        encode(sha256('tos-mycaffe-azuredevid-v1.0.0-id'::bytea), 'hex')
    ),
    (
        'privacy_policy_pdp', '1.0.0', 'id-ID',
        'Kebijakan Privasi & Perlindungan Data Pribadi (UU No. 27 Tahun 2022)',
        'Pemrosesan data pribadi pelanggan dan staf gerai dilindungi dengan isolasi Row-Level Security PostgreSQL 18 dan enkripsi kredensial AES-256-GCM sesuai UU PDP No. 27/2022.',
        encode(sha256('pdp-mycaffe-azuredevid-v1.0.0-id'::bytea), 'hex')
    ),
    (
        'dpa_tenant', '1.0.0', 'id-ID',
        'Perjanjian Pemrosesan Data Mitra Usaha (Data Processing Agreement)',
        'Mitra Usaha bertindak sebagai Pengendali Data Pribadi pelanggan gerai dan menunjuk AzureDevID sebagai Prosesor Data Pribadi.',
        encode(sha256('dpa-mycaffe-azuredevid-v1.0.0-id'::bytea), 'hex')
    ),
    (
        'refund_policy', '1.0.0', 'id-ID',
        'Kebijakan Pembatalan dan Pengembalian Dana Langganan SaaS',
        'Langganan SaaS dapat dibatalkan kapan saja sebelum jatuh tempo berikutnya. Kelebihan bayar atau duplikasi transaksi dikembalikan 100% dalam 5-7 hari kerja.',
        encode(sha256('refund-mycaffe-azuredevid-v1.0.0-id'::bytea), 'hex')
    )
ON CONFLICT (doc_type, version, locale) DO NOTHING;

-- Backfill existing stores into platform_tenant_registry idempotently and auto-sync future stores
INSERT INTO platform_tenant_registry (store_id, slug, display_name, plan_code, subscription_status, kyb_status)
SELECT id, slug, name, 'pro_cafe', 'active', 'verified'
FROM stores
ON CONFLICT (store_id) DO NOTHING;

CREATE OR REPLACE FUNCTION fn_sync_store_to_platform_registry()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO platform_tenant_registry (store_id, slug, display_name, plan_code, subscription_status, kyb_status, updated_at)
    VALUES (NEW.id, NEW.slug, NEW.name, 'trial_14d', 'trialing', 'unverified', NOW())
    ON CONFLICT (store_id) DO UPDATE SET
        slug = EXCLUDED.slug,
        display_name = EXCLUDED.display_name,
        updated_at = NOW();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_stores_platform_registry ON stores;
CREATE TRIGGER trg_stores_platform_registry
    AFTER INSERT OR UPDATE OF slug, name ON stores
    FOR EACH ROW
    EXECUTE FUNCTION fn_sync_store_to_platform_registry();
