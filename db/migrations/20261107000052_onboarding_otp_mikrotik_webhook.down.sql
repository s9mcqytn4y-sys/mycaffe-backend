-- 20261107000052_onboarding_otp_mikrotik_webhook.down.sql

DROP FUNCTION IF EXISTS provision_store_from_onboarding(UUID, UUID, UUID, TEXT, TEXT);
DROP FUNCTION IF EXISTS resolve_platform_invoice_webhook_context(VARCHAR);

DROP TABLE IF EXISTS store_mikrotik_profiles;

ALTER TABLE governance_audit_logs
    DROP CONSTRAINT IF EXISTS chk_gov_audit_action;
ALTER TABLE governance_audit_logs
    ADD CONSTRAINT chk_gov_audit_action CHECK (action IN (
        'LEGAL_ACCEPTED', 'KYB_UPDATED', 'KYB_REVIEWED',
        'BYOK_MIDTRANS_UPDATED', 'BYOK_MIDTRANS_VERIFIED', 'BYOK_MIDTRANS_DISABLED',
        'STORE_CMS_UPDATED', 'NOTIFICATION_CONFIG_UPDATED',
        'NETWORK_NODE_UPSERTED', 'NETWORK_SIMULATION_RUN',
        'CLEARANCE_REQUESTED', 'CLEARANCE_CANCELLED', 'CLEARANCE_EXECUTED',
        'PLATFORM_INVOICE_CREATED', 'PLATFORM_INVOICE_SETTLED'
    ));

ALTER TABLE governance_audit_logs
    DROP CONSTRAINT IF EXISTS chk_gov_audit_actor_type;
ALTER TABLE governance_audit_logs
    ADD CONSTRAINT chk_gov_audit_actor_type CHECK (actor_type IN (
        'tenant_owner', 'tenant_staff', 'platform_admin', 'system'
    ));

UPDATE tenant_onboarding_applications
SET status = 'submitted'
WHERE status = 'pending_email_otp';

ALTER TABLE tenant_onboarding_applications
    DROP CONSTRAINT IF EXISTS chk_onboarding_otp_attempts,
    DROP CONSTRAINT IF EXISTS chk_onboarding_otp_hash,
    DROP CONSTRAINT IF EXISTS chk_onboarding_status;

ALTER TABLE tenant_onboarding_applications
    ADD CONSTRAINT chk_onboarding_status
    CHECK (status IN ('submitted', 'under_review', 'approved', 'rejected'));

ALTER TABLE tenant_onboarding_applications
    DROP COLUMN IF EXISTS email_verified_at,
    DROP COLUMN IF EXISTS email_otp_attempts,
    DROP COLUMN IF EXISTS email_otp_sent_at,
    DROP COLUMN IF EXISTS email_otp_expires_at,
    DROP COLUMN IF EXISTS email_otp_hash;
