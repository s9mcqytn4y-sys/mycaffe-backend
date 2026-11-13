-- db/seeds/development.sql
-- Global Platform Reference Seed for Cassy by AzureDevID.
--
-- POLICY: ZERO dummy tenants (stores, users, categories, products, tables, or orders) are seeded here.
-- Every tenant store in local development, public demo, and staging must be created through:
--   1. Self-Service Onboarding + 6-Digit Email OTP (/onboarding -> POST /api/v1/public/onboarding/apply
--      -> POST /api/v1/public/onboarding/:id/verify-otp -> POST /api/v1/platform/onboarding/:id/provision), OR
--   2. Break-glass operator CLI: go run ./cmd/migrate -action=bootstrap-store
--
-- Automated CI integration test fixtures (central-cafe, express-counter) live exclusively in
-- db/seeds/test_fixtures.sql and are executed only when APP_ENV=test.

INSERT INTO subscription_plans (
    code, name_id, name_en, monthly_price_idr, annual_price_idr, commission_bps,
    max_tables, max_printers, supports_byok_midtrans, supports_kds_queue_tv, supports_whitelabel, supports_byo_notifications
) VALUES
    ('trial_14d', 'Uji Coba Gratis 14 Hari', '14-Day Free Trial', 0, 0, 0, 15, 1, true, true, false, false),
    ('starter_umkm', 'Starter UMKM', 'Starter SME', 149000, 1430400, 0, 15, 1, true, false, false, false),
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
        'Syarat dan Ketentuan Layanan SaaS Cassy by AzureDevID',
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
    ),
    (
        'terms_of_service', '1.0.0', 'en-US',
        'Cassy by AzureDevID SaaS Terms of Service',
        'AzureDevID operates strictly as a pure Software-as-a-Service (SaaS) provider using Bring Your Own Key (BYOK) gateway credentials with 0% transaction commission. Customer menu payments settle directly to each Tenant merchant account.',
        encode(sha256('tos-mycaffe-azuredevid-v1.0.0-en'::bytea), 'hex')
    ),
    (
        'privacy_policy_pdp', '1.0.0', 'en-US',
        'Privacy Policy & Personal Data Protection (Indonesia Law No. 27/2022)',
        'Guest and staff personal data are isolated per tenant using PostgreSQL 18 Row-Level Security and AES-256-GCM credential encryption in compliance with Indonesia PDP Law No. 27/2022.',
        encode(sha256('pdp-mycaffe-azuredevid-v1.0.0-en'::bytea), 'hex')
    )
ON CONFLICT (doc_type, version, locale) DO UPDATE SET
    title = EXCLUDED.title,
    content_markdown = EXCLUDED.content_markdown;
