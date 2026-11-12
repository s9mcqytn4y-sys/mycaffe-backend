-- 20261112000061_legal_v1_final.up.sql
-- Legal review done (PRD v4 W3). Guest texts become final 1.1.0: the reviewed 1.1.0-draft text without its draft line.
-- The tenant terms get 1.0.1 with the Cassy title; the body is unchanged, so its hash and every tenant acceptance stay valid.

INSERT INTO legal_documents (doc_type, version, locale, title, content_markdown, content_sha256)
SELECT doc_type, '1.1.0', locale, title, body, encode(sha256(convert_to(body, 'UTF8')), 'hex')
FROM (
    SELECT doc_type, locale, title,
           regexp_replace(content_markdown, '^(Draf, menunggu tinjauan penasihat hukum\.|Draft, pending review by legal counsel\.)\n\n', '') AS body
    FROM legal_documents
    WHERE doc_type IN ('guest_privacy_notice', 'guest_order_terms') AND version = '1.1.0-draft'
) d
ON CONFLICT (doc_type, version, locale) DO NOTHING;

UPDATE legal_documents SET is_active = false
WHERE doc_type IN ('guest_privacy_notice', 'guest_order_terms') AND version = '1.1.0-draft';

INSERT INTO legal_documents (doc_type, version, locale, title, content_markdown, content_sha256)
SELECT doc_type, '1.0.1', locale, 'Syarat dan Ketentuan Layanan SaaS Cassy oleh AzureDevID', content_markdown, content_sha256
FROM legal_documents
WHERE doc_type = 'terms_of_service' AND version = '1.0.0'
ON CONFLICT (doc_type, version, locale) DO NOTHING;

UPDATE legal_documents SET is_active = false
WHERE doc_type = 'terms_of_service' AND version = '1.0.0';
