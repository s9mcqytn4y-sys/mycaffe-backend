---
name: signed-qr-table-session
description: Design or review MyCaffe signed printed-table QR credentials, QR generator, guest table sessions, revocation, and guest order isolation.
disable-model-invocation: true
---

# Signed QR Table Session and Generator

## Required source review
Read CLAUDE.md, AGENTS.md, docs/product/module-1-customer-ordering.md, docs/product/module-3-admin-backoffice.md, docs/architecture/phase-01-database-contract.md, docs/architecture/pos-use-cases-and-sequences.md, internal/handler/order_handler.go, cmd/api/main.go, current order migrations, and relevant integration tests.

## Keep three credentials separate
1. Staff access token: identifies a staff user and carries the staff audience and permissions.
2. Printed table QR credential: a public, revocable capability identifying one store/table. It is not a staff identity and must not authorize POS, finance, or backoffice actions.
3. Guest table-session token: a short-lived session created after a valid table QR exchange. It authorizes only the guest actions explicitly scoped to that session.

## Security requirements
- Bind a QR credential to token purpose, issuer, audience, key ID, store UUID, table UUID, and the current QR generation/version. Verify the signature before using claims to establish tenant context.
- Use a dedicated signing key/key ring for table QR credentials; do not reuse the staff JWT secret. Support key rotation and immediate table-level revocation.
- A printed QR may need to remain usable for long periods. Do not invent an expiry policy; compare expiry versus version-based revocation and obtain product approval.
- A scan must validate that the store and table are active and QR ordering is enabled. Emergency pause and inactive/revoked QR behavior must be explicit.
- Create a distinct guest session per scan/device unless product explicitly chooses shared sessions. Support two guests ordering from the same table without granting either guest access to the other's order or personal data.
- Derive store_id and table_id from the verified QR/session context, never from request-body fields, URL query parameters, or browser storage.
- Do not put bearer tokens in URL query strings, logs, analytics, referrer-bearing links, or error messages. Prefer POST body/header exchange and no-store responses.
- Guest endpoints must not accept staff JWTs as substitutes or expose owner/cashier/kitchen data.
- Preserve order-to-table and order-to-session relationships with composite store-scoped foreign keys and RLS. Keep a table-number snapshot only where needed for historical receipts.
- QR rotation should invalidate new exchanges using the old QR version. Decide separately whether already-created guest sessions remain active.
- The backend should return a validated QR URL/token payload and metadata. QR image rendering and branded print layout may belong in the separate frontend repository; confirm before adding image-generation dependencies to this backend.

## Required deliverable
Produce a lifecycle sequence, token claim contract, endpoint contract, schema proposal, abuse-case matrix, multi-device behavior, QR rotation/revocation behavior, print-generator boundary, and integration test plan.

Do not implement token signing, QR generation, new tables, public order routes, or key-management changes until the user approves the design decisions.
