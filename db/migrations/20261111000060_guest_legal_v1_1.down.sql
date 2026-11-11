-- 20261111000060_guest_legal_v1_1.down.sql
-- Shows guest texts v1.0 again and removes only the v1.1 rows this migration added.

UPDATE legal_documents
SET is_active = true
WHERE doc_type IN ('guest_privacy_notice', 'guest_order_terms') AND version = '1.0.0-draft';

DELETE FROM legal_documents
WHERE doc_type IN ('guest_privacy_notice', 'guest_order_terms') AND version = '1.1.0-draft';
