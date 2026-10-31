# Phase 02 Sub-task 01 — Authentication, RBAC, Signed QR Table Session & Generator

**Status:** Design review draft — not an implementation authorization  
**Repository baseline reviewed:** PR #5 head, commit 19bd992  
**Scope:** Staff identity, staff authorization, printed table QR credentials, guest table sessions, and the handoff into customer ordering.

## 1. Executive assessment

Phase 02 must establish two separate trust domains:

1. Staff trust: who is the staff member, which store do they belong to, and which operations can they perform?
2. Guest/table trust: does the customer possess a valid credential for an active table, and which guest session/order data may that credential access?

A printed table QR is a public bearer capability. It is not a staff login and must never grant POS, owner, finance, or KDS privileges. A guest table session is created only after the server validates the printed QR credential. Staff JWTs, printed QR credentials, and guest session tokens must have separate purposes and audiences; use separate signing keys or a deliberately managed key ring.

Do not enable public order/payment routes in staging or production until identity binding, tenant context, RLS, and end-to-end tests are complete.

## 2. Current repository findings

The following findings are based on the repository at commit 19bd992:

| Area | Current implementation | Consequence |
|---|---|---|
| Staff table | users has store_id, email, full_name, role, pin_hash, is_active | Staff identity data exists; no login flow is wired |
| Roles | owner, cashier, barista, kitchen are enforced by the domain and database check | Preserve these four roles; do not create a second role system |
| JWT validation | HS256, required exp, issuer, audience, UUID store/user claims, role enum | Validation exists, but it is not a complete authentication/session system |
| JWT issuance | GenerateTestToken is test-oriented | A production issuer and refresh/revocation lifecycle are not present |
| Session revocation | No session ID/JTI or auth-session persistence in the current claims model | Logout, staff deactivation, and role-change revocation need a design |
| Router | cmd/api/main.go registers order, payment webhook, health, and WebSocket routes; no staff login or authentication middleware is wired | Authentication and authorization are not yet active on API route groups |
| Request identity | Order/payment request structs include client-supplied store_id and actor_user_id | These fields must not remain trusted inputs after auth is wired |
| Production containment | Customer order creation, payment initiation, and WebSocket are fail-closed in staging/production | Preserve this protection until each route has the correct identity type |
| Table model | orders.table_number is a plain string; no tables table or table_id relationship was found | Table lifecycle and QR generation require a new tenant-scoped model |
| Guest session | No table_sessions entity or guest session exchange route was found | A scan cannot yet create a revocable, scoped guest session |
| PIN hashing | users.pin_hash exists; seed values are placeholders; no production PIN verifier was found | Choose and test the hashing format before relying on the field |
| Entity naming | Current schema/domain use users; Module 3 lists staff_users among suggested entities | Keep users as the current canonical table and do not create staff_users as a duplicate without an approved migration decision |

### Existing JWT details and gaps

The current validator already rejects non-HS256 algorithms, missing expiration, invalid issuer/audience, malformed store/user UUIDs, and unknown roles. It does not currently provide:
- A production token issuer.
- A token purpose/type claim to separate staff and guest tokens.
- A session identifier/JTI checked against server-side session state.
- Refresh-token rotation, reuse detection, logout, or revocation.
- Immediate invalidation after staff deactivation or role change.
- A distinct signing key contract for table QR and guest sessions.

JWT_EXPIRED_HOURS currently defaults to 24. That duration should be reconsidered when the refresh/revocation model is chosen.

## 3. Trust boundaries

### Staff request

Browser/POS client → staff login → credential verification → staff session → signed staff access token → authentication middleware → permission policy → handler → service → WithTenantTx → PostgreSQL RLS.

The server derives store_id, user_id, and role from verified server-side identity/session state. Request-body identity fields are removed from authenticated contracts. The service must receive a trusted actor context, not a caller-provided actor UUID.

### Guest QR request

Printed QR → public scan/exchange endpoint → signature and table-state validation → guest table session → guest-scoped order API → order/session ownership check → WithTenantTx → PostgreSQL RLS.

The printed QR identifies an eligible table, not a person. The guest session authorizes only the guest actions and order records explicitly bound to that session.

### Separate token purposes

| Credential | Purpose | Authority |
|---|---|---|
| Staff access token | Authenticated staff API access | Permissions from current staff role/session |
| Printed table QR credential | Start a guest session for one table | No staff permissions; only table-session exchange |
| Guest table-session token | Guest ordering and own-order tracking | No access to other guest sessions or staff endpoints |
| WebSocket ticket | One-time/short-lived realtime connection | Separate Phase 03 contract; not a long-lived staff or QR token |

Do not put bearer credentials in query strings. Do not log raw credentials. Use no-store responses for token exchange.

## 4. Proposed staff authentication design

### Proposed login contract

Candidate endpoint: POST /api/v1/auth/staff/login

Candidate request fields:
- store_slug
- email or a dedicated staff code
- PIN

Candidate response:
- short-lived staff access token
- refresh token returned securely
- staff profile limited to id, name, role, and store context
- access/session expiry metadata

The current schema supports store-scoped email and pin_hash, but does not include password_hash or a persisted staff_code. The brand guide defines a staff ID-card display pattern (STF-{STORE_SLUG_UPPER}-{XXXX}) based on users.id, but does not specify a stored login identifier column. Therefore decide whether login uses email or a derived/persisted staff code before implementation.

### Login security

- Use a generic authentication failure response so callers cannot distinguish unknown store, unknown email, inactive user, locked account, or incorrect PIN.
- Apply IP and account/store-scoped rate limits, persistent lockout/backoff, and audit events.
- Hash PIN/password material with an approved slow password-hashing algorithm. A pepper, if selected, belongs in a secret manager/environment and never in the database or repository.
- Do not treat a PIN as a strong password. Choose minimum PIN length and lockout thresholds with the owner; test brute-force and enumeration behavior.
- Use a narrowly scoped pre-authentication store resolver for store_slug if RLS prevents lookup before tenant context. Do not add a broad SECURITY DEFINER bypass.
- Lock or update login-attempt state transactionally to avoid concurrent attempts bypassing lockout.

### Proposed session lifecycle

- Issue a short-lived access token with issuer, audience, subject/user, store, role, issued-at, expiry, token purpose, and session identifier.
- Persist a server-side auth session. Store only a hash of the refresh token, never the raw token.
- Rotate refresh tokens on every refresh and detect reuse. Revoke the session on logout.
- Revoke all relevant sessions when a staff account is deactivated, PIN reset, or role changed.
- Revalidate active user/session state at the server boundary; do not rely on a 24-hour role claim remaining correct.
- Decide whether a staff member may have multiple devices/sessions and define idle and absolute session expiry.

Candidate companion endpoints:
- POST /api/v1/auth/staff/refresh
- POST /api/v1/auth/staff/logout
- GET /api/v1/auth/staff/me

These are proposed contracts, not existing routes.

## 5. Proposed RBAC model

Keep the four existing roles and implement one authoritative backend permission policy. UI visibility is not authorization.

| Capability | owner | cashier | barista | kitchen | guest table session |
|---|---|---|---|---|---|
| Store settings, staff, role/PIN management | Full | No | No | No | No |
| Catalog, pricing, cost/HPP, financial reports | Full | Operational fields only if explicitly approved | No | No | Public menu fields only |
| Create POS/walk-in orders | Yes | Yes | No | No | No |
| Create QR table orders | Staff path only | Staff path only | No | No | Own session only |
| Cash, static QRIS, EDC, and dynamic QRIS payment operations | Yes | Yes | No | No | Only guest flow tied to its own order |
| Open tab, split bill, cashier shift open/close | Yes | Yes | No | No | No |
| KDS ticket visibility | All stations | Operational visibility as approved | Barista station | Kitchen station | No |
| KDS status transition | All stations | As approved | Barista station only | Kitchen station only | No |
| Quick item availability toggle | All | No by default | Beverage items only | Food/pastry items only | No |
| Manage tables and QR generation/rotation/revocation | Yes | No by default | No | No | No |
| Void/refund paid transaction | Yes | Configured authorization flow plus mandatory reason; exact PIN/owner-override policy unresolved | No | No | No |
| View another tenant's data | Never | Never | Never | Never | Never |
| View another guest session's order/contact data | No guest access | Staff permissions only | No | No | No |

This matrix is provisional. The product sources contain a specific void/refund policy conflict: Module 3 says a cashier must not void/refund a paid transaction without a configured authorization flow, while the brand guide says cashier can void/refund with mandatory reason logging and rejects multi-step manager approval bottlenecks. Do not silently choose one interpretation. Confirm whether the configured flow means cashier PIN re-authentication, owner PIN override, a transaction threshold, or another single-step rule. The product docs also specify cashier open_tab, split bill, shift operations, and station-specific quick availability toggles. Discounts and stock override limits still need explicit confirmation.

### Enforcement pattern

- Authentication middleware validates token signature, issuer, audience, purpose, expiry, session state, and current staff state.
- Authorization middleware checks explicit permission constants.
- Critical service operations repeat the necessary business authorization checks.
- Never trust a role sent in JSON, query parameters, or headers.
- Keep the domain role enum and JWT role validation aligned; remove duplicate role sources where practical.
- Preserve audit logging for role changes, PIN resets, price/stock changes, void/refund, shift discrepancies, and sensitive payment actions.

## 6. Proposed table and QR model

### Table entity

The product docs already describe table management, but the database currently has no table entity. Candidate tenant-scoped table fields:

- id, store_id
- table_number and display_name
- capacity
- is_active
- qr_ordering_enabled
- qr_version (monotonic generation number)
- created_at, updated_at

Enforce unique(store_id, table_number) and unique(store_id, id). Add composite foreign keys for all references to a table from guest sessions and orders.

### Printed QR credential

Recommended claim contract for a signed table credential:

- issuer and dedicated audience
- explicit token purpose/type
- key ID (kid)
- store UUID and table UUID
- current qr_version
- issued-at timestamp
- HMAC-SHA256 signature using a dedicated table-QR signing key/key ring

Do not reuse JWT_SECRET_KEY for table QR signing. Do not include customer identity or personal information.

A printed QR may need to remain usable for months. A fixed short expiry would force reprinting. Prefer version-based revocation for a static printed QR, with key-ID-based signing-key rotation. The business must still approve the reprint/expiry policy. A new QR version invalidates old printed QR codes for future session creation.

### QR generator

Candidate owner-only endpoints:
- POST /api/v1/tables
- GET /api/v1/tables
- PATCH /api/v1/tables/:id
- POST /api/v1/tables/:id/qr
- POST /api/v1/tables/:id/qr/rotate
- POST /api/v1/tables/:id/qr/revoke

The backend should return a signed ordering URL/token and metadata only to an authorized owner. QR generation, rotation, and revocation must be audited. Raw QR credentials must not appear in logs or analytics.

The backend repository does not contain the customer-facing Next.js implementation. Confirm whether QR PNG/SVG generation and branded print layouts belong in the frontend repository. Avoid adding a QR image library to the backend unless the API contract requires it.

## 7. Proposed guest table-session lifecycle

Candidate exchange endpoint: POST /api/v1/public/table-sessions

1. Client scans the printed table QR.
2. Client submits the QR credential to the exchange endpoint in a POST body.
3. Server validates signature, token purpose/audience, key ID, table/store state, and QR version.
4. Server creates a new guest table-session record.
5. Server issues a short-lived guest session token with a distinct audience and purpose.
6. Guest order creation derives store_id and table_id from the validated session, not the request body.
7. Every guest order is associated with table_id and table_session_id.
8. Guest tracking exposes only the order(s) created by that session and only the minimum public fields.

Create separate sessions for separate devices by default. The product explicitly requires two guests at one table to order from separate devices. Do not let one guest session enumerate other orders from the same table. The product also requires a defined behavior when a customer closes/reopens the browser or scans the same QR again; decide whether the client resumes a still-valid session or creates a new session.

Candidate table_sessions fields:
- id, store_id, table_id
- status
- created_at, last_activity_at, expires_at, revoked_at
- qr_version_at_creation

Candidate order additions:
- table_id (nullable for takeaway)
- table_session_id (nullable for staff-originated/non-QR orders)
- retain table_number as a historical display snapshot if required

Define whether QR rotation invalidates only new scans or also active guest sessions. Define guest idle timeout, absolute lifetime, and behavior when the store pauses QR ordering.

## 8. API route separation

Avoid a single ambiguous order route that accepts either a staff token or a guest token without an explicit policy.

Proposed route groups:
- Staff: /api/v1/auth/staff/*, /api/v1/staff/*, /api/v1/pos/*
- Owner table management: /api/v1/tables/*
- Public guest: /api/v1/public/table-sessions, /api/v1/public/orders/*
- Provider webhook: remains separate and uses provider signature verification, not staff JWT.

Remove caller-controlled store_id and actor_user_id from authenticated request contracts. The trusted store and actor are supplied to services from middleware context.

The current /ws endpoint is explicitly contained in staging/production pending Phase 03 session-ticket validation. Keep WebSocket ticket design as a separate follow-on; do not reuse the static QR token in a query string.

## 9. Migration and implementation sequence

Break the work into reviewable increments rather than one large security patch:

| Increment | Scope | Exit condition |
|---|---|---|
| 02.01-A | Freeze login, session, role, and guest QR decisions; ADR/API contracts | Approved threat model and permission matrix |
| 02.01-B | Staff credential verification, login throttling, auth_sessions, token issuer/refresh/revocation | Auth integration tests pass under mycaffe_app and RLS |
| 02.01-C | Authentication middleware, trusted actor context, RBAC policy, owner staff management | Table-driven permission tests and cross-tenant denial pass |
| 02.01-D | tables schema, QR signing/key ID/version, owner generator/rotation/revoke | Tamper, inactive table, stale version, key rotation tests pass |
| 02.01-E | table_sessions, guest token, order/session binding, public tracker isolation | Multi-device and cross-session integration tests pass |
| 02.01-F | OpenAPI, security review, CI, staging verification and rollout | All three CI gates pass; no public route is opened prematurely |

Each migration must be additive, reversible, PostgreSQL 18-compatible, and tested through migration up/down/re-up, RLS verification, and integration tests as mycaffe_app.

## 10. Required security test matrix

### Staff authentication
- Correct and incorrect credentials; generic failure response.
- Unknown store/email, inactive user, locked account, and concurrent failed attempts.
- Rate-limit behavior by IP and account/store.
- Invalid algorithm, signature, issuer, audience, purpose, expiry, UUID, role, missing session ID.
- Refresh rotation, replay detection, logout, session expiry, staff deactivation, PIN reset, role change.
- Last active owner cannot be accidentally removed or deactivated.

### RBAC and tenant isolation
- Every role against every permission.
- Cross-tenant object ID substitution for users, orders, tables, payments, and reports.
- Cashier cannot read owner-only cost/finance/staff data.
- Barista and kitchen cannot read or mutate the other station's tickets.
- Guest session cannot access staff endpoints or another guest's order.
- All integration tests run under mycaffe_app with RLS enabled.

### QR and guest session
- Tampered signature, unknown kid, wrong audience/purpose, invalid UUIDs.
- Rotated/revoked QR version, inactive store/table, QR ordering paused.
- Multiple guests/devices at one table.
- Expired/revoked guest session and replay attempts.
- Guest session A cannot read guest session B's order/contact data.
- QR rotation behavior for existing guest sessions is explicitly tested.
- Raw QR/session tokens are absent from logs and telemetry.

## 11. Decisions required before implementation

1. Staff login identity: store slug + email + PIN, store slug + staff ID-card code + PIN, or owner password plus staff PIN? If using staff code, derive it from users.id or add a persisted unique code?
2. PIN policy: minimum digits, hashing algorithm, retry threshold, lockout duration, and whether PIN is for login, sensitive-action confirmation, or both.
3. Session policy: multiple devices per staff user, access-token TTL, refresh-token TTL, idle timeout, and forced logout behavior.
4. Permission policy: exact cashier powers for discount, void/refund, stock override, shift close, and open-tab/split bill.
5. Printed QR lifecycle: no fixed expiry with version revocation, or scheduled expiry/reprint? Who may rotate/revoke?
6. Guest lifecycle: idle/absolute TTL, multiple guest sessions per table, browser close/reopen and QR rescan behavior, and whether QR rotation leaves existing sessions active.
7. QR generator output: backend URL/token metadata only, or backend-generated SVG/PNG? Confirm the frontend repository boundary.
8. Table lifecycle: table numbering, capacity, merge/split, temporary closure, and emergency store-wide QR ordering pause.

## 12. Go/no-go criteria

Do not enable public ordering or authenticated POS routes in staging/production until:
- Staff login, refresh, logout, revocation, and role-change invalidation are complete.
- Every protected route has an explicit authentication type and permission.
- Request-body tenant/actor identity is removed from trusted paths.
- Table QR credentials and guest session tokens are cryptographically and semantically separated.
- Guest order access is bound to the originating session.
- PostgreSQL RLS and composite tenant constraints pass under mycaffe_app.
- Security, race, migration, Docker, and integration gates pass.

This document is a proposed design review. It does not authorize schema changes, role changes, token-key rotation, production configuration changes, or implementation.
