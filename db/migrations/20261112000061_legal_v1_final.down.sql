-- 20261112000061_legal_v1_final.down.sql
-- Shows the 1.1.0-draft guest texts and the 1.0.0 tenant terms again, and removes only the rows this migration added.

UPDATE legal_documents SET is_active = true
WHERE (doc_type IN ('guest_privacy_notice', 'guest_order_terms') AND version = '1.1.0-draft')
   OR (doc_type = 'terms_of_service' AND version = '1.0.0');

DELETE FROM legal_documents
WHERE (doc_type IN ('guest_privacy_notice', 'guest_order_terms') AND version = '1.1.0')
   OR (doc_type = 'terms_of_service' AND version = '1.0.1');
