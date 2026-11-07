-- 20261107000051_platform_saas_governance.down.sql
-- Safe teardown of migration 000051: refuses rollback if paid SaaS invoices, active BYOK keys,
-- legal acceptances, or executed clearance records exist.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM tenant_legal_acceptances)
       OR EXISTS (SELECT 1 FROM platform_invoices WHERE status = 'paid')
       OR EXISTS (SELECT 1 FROM store_payment_credentials WHERE is_enabled = true)
       OR EXISTS (SELECT 1 FROM tenant_clearance_requests WHERE status = 'pii_anonymized_ledger_retained') THEN
        RAISE EXCEPTION 'Active governance, legal acceptance, paid SaaS invoice, or BYOK records exist; cannot roll back 000051.';
    END IF;
END $$;

DROP TRIGGER IF EXISTS trg_stores_platform_registry ON stores;
DROP FUNCTION IF EXISTS fn_sync_store_to_platform_registry();

DROP TABLE IF EXISTS governance_audit_logs;
DROP TABLE IF EXISTS store_network_nodes;
DROP TABLE IF EXISTS store_notification_configs;
DROP TABLE IF EXISTS store_cms_configs;
DROP TABLE IF EXISTS store_payment_credentials;
DROP TABLE IF EXISTS tenant_clearance_requests;
DROP TABLE IF EXISTS platform_invoices;
DROP TABLE IF EXISTS store_kyb_profiles;
DROP TABLE IF EXISTS tenant_legal_acceptances;
DROP TABLE IF EXISTS platform_tenant_registry;
DROP TABLE IF EXISTS tenant_onboarding_applications;
DROP TABLE IF EXISTS subscription_plans;
DROP TABLE IF EXISTS legal_documents;
