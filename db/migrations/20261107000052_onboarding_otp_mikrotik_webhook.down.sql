-- 20261107000052_onboarding_otp_mikrotik_webhook.down.sql

DROP FUNCTION IF EXISTS provision_store_from_onboarding(UUID, UUID, UUID, TEXT, TEXT);
DROP FUNCTION IF EXISTS resolve_platform_invoice_webhook_context(VARCHAR);

DROP TABLE IF EXISTS store_mikrotik_profiles;

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
