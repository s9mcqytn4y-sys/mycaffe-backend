-- 20261110000058_guest_legal_documents.down.sql
-- Removes only the guest documents this migration added, then restores the original type list.

DELETE FROM legal_documents WHERE doc_type IN ('guest_privacy_notice', 'guest_order_terms');

ALTER TABLE legal_documents
    DROP CONSTRAINT chk_legal_documents_type,
    ADD CONSTRAINT chk_legal_documents_type CHECK (doc_type IN ('terms_of_service', 'privacy_policy_pdp', 'dpa_tenant', 'refund_policy'));
