# ADR-003: Staff Authentication, Session Lifecycle & RBAC Policy (Phase 02 Sub-task 01-A)

## Status
**PROPOSED** — owner decisions recorded on 2026-10-31; awaiting owner approval of this document before increment 02.01-B (implementation) starts.

- **Baseline:** PR #5 head `19bd992` (`feature/phase-01-subtask-04-payment-ledger`).
- **Companion:** Phase 02 design review draft (PR #6, `docs/architecture/phase-02-subtask-01-auth-rbac-signed-qr-design-review.md`).
- **Scope of this ADR:** staff identity, staff sessions, RBAC permission policy, route authentication classes. Table QR credentials and guest table sessions (increments 02.01-D/E) are referenced only where they constrain staff-auth design; they get their own ADR.
- **Not authorized by this ADR:** schema changes, new routes, token-key rotation, or opening any contained route in staging/production. Those happen in 02.01-B/C after approval.

Classification used below: *Implemented & Tested*, *Guarded / Fail-Closed*, *Planned*, *Blocked by Dependency*.

## 1. Context

### 1.1 What exists today (verified against `19bd992`)

| Area | State | Classification |
|---|---|---|
| `users` table | `store_id`, `email`, `full_name`, `role`, `pin_hash`, `is_active`; `UNIQUE(store_id, email)`; `CHECK role IN (owner, cashier, barista, kitchen)`; RLS `store_id = current_store_id()` | Implemented & Tested |
| `stores` RLS | `USING (id = current_store_id())` — `mycaffe_app` cannot read any store row before tenant context is set | Implemented & Tested |
| JWT validation (`internal/pkg/auth/token.go`) | HS256 only, required `exp`, `iss=mycaffe-backend`, `aud=mycaffe-pos`, UUID `store_id`/`user_id`, role enum | Implemented & Tested |
| JWT issuance | Only `GenerateTestToken`; no production issuer, no `sid`/`jti`/purpose claim, no refresh/revocation | Planned |
| Config | `JWT_SECRET_KEY` (≥32 chars, fail-closed in production), `JWT_EXPIRED_HOURS` default 24 | Implemented |
| Routes | `POST /api/v1/orders`, `POST /api/v1/orders/:id/payments/initiate`, `GET /ws` fail-closed (`503`) in staging/production; webhook protected by Midtrans SHA-512 | Guarded / Fail-Closed |
| Request identity | `CreateOrderRequest.store_id` and `InitiateOrderPaymentRequest.store_id` / `actor_user_id` are client-supplied | Guarded (contained routes only) |
| Audit | `audit_logs` with composite FK `(store_id, actor_id) → users(store_id, id)`; action CHECK has no auth/session actions | Implemented |
| Staff login, middleware, RBAC policy | None wired | Planned |

### 1.2 New findings not covered by the design review

| # | Finding | Impact | Disposition |
|---|---|---|---|
| F1 | Pre-auth `store_slug → store_id` lookup is impossible for `mycaffe_app` because of `stores` RLS. | Login cannot resolve the tenant without either a bypass or a dedicated resolver. | D5: narrowly-scoped `SECURITY DEFINER` resolver returning only `(id, is_active)`. |
| F2 | Gin trusted proxies are not configured (`router.SetTrustedProxies` is never called), so `c.ClientIP()` honours any client-supplied `X-Forwarded-For`. | Per-IP login throttling and IP logging can be spoofed/bypassed. | Must be fixed in 02.01-B **before** per-IP limits are relied upon: trust only the Nginx container network, read `X-Real-IP` set by Nginx. |
| F3 | `GET /ws` accepts the staff JWT via `?token=` query string (dev only today). | Once real access tokens exist, they would land in Nginx access logs and browser history. | 02.01-C removes query-string acceptance of staff access tokens; WebSocket gets a separate one-time ticket in Phase 03. Route stays contained until then. |
| F4 | `uq_users_store_email` is case-sensitive. | `Owner@x.id` and `owner@x.id` can coexist; login lookup becomes ambiguous. | Normalize emails to `lower(trim(email))` at write and lookup; add `UNIQUE (store_id, lower(email))` after a duplicate pre-check. |
| F5 | `JWT_EXPIRED_HOURS=24` is incompatible with the approved 15-minute access TTL. | Long-lived bearer tokens survive role change/deactivation. | Deprecate; replace with `STAFF_ACCESS_TOKEN_TTL` / `STAFF_REFRESH_TOKEN_TTL` (D4). |
| F6 | `audit_logs.store_id` is `NOT NULL`. | Failed logins against an unknown/invalid store cannot be written to `audit_logs`. | Unknown-store failures go to structured application logs only (no raw email, see §6). |
| F7 | Seed `pin_hash` values are placeholders. | No seeded user can log in once a real verifier exists. | 02.01-B seed generates argon2id hashes for development-only PINs; production never seeds PINs. |

## 2. Decisions

### D1 — Login identity: `store_slug + email + PIN` *(owner decision 2026-10-31)*
- Endpoint: `POST /api/v1/auth/staff/login` with body `{store_slug, email, pin}`.
- `email` is normalized to `lower(trim(email))` before lookup; the same normalization is enforced on user create/update.
- No `password_hash` or `staff_code` column is added. Owner and operational staff use the same mechanism; owner-only actions are protected by RBAC and PIN re-authentication (D6), not by a second credential type.
- **Accepted trade-off:** a 6-digit PIN is a weak secret (10⁶ space). Security rests on server-side throttling/lockout (D3), not on PIN entropy. Revisit (owner password or TOTP for owner role) if the platform hosts high-value merchants.

### D2 — PIN policy & hashing *(owner decision: "Ketat")*
- Numeric only, **6–12 digits**. Reject trivially weak PINs at set/reset time: all-identical digits, ascending/descending runs (`123456`, `654321`), and a small denylist (`000000`, `111111`, `121212`, `112233`, birth-year-like `19xx19`).
- Hash: **argon2id** via `golang.org/x/crypto/argon2` (already in `go.sum` as an indirect dependency at v0.58.0 — promoted to direct, no new module). Parameters: `m=19456 KiB, t=2, p=1`, 16-byte random salt, 32-byte key, stored as a PHC string (`$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>`) in the existing `pin_hash VARCHAR(255)`.
- Parameters are read from the PHC string on verify, so they can be raised later; a successful login with outdated parameters transparently re-hashes.
- **Concurrency cap:** a process-wide semaphore limits concurrent argon2id computations (default 4) so login floods cannot exhaust RAM/CPU on the 2 vCPU / 8 GB VPS. Excess requests get `429` before hashing.
- **Timing equalization:** unknown store, unknown email, and inactive user all run one argon2id verification against a fixed dummy hash before returning the generic failure.
- **No pepper in v1.** A lost pepper would lock every staff account across all tenants on a single VPS with no HSM. Can be added later (HMAC-SHA256 pre-hash, key-ID prefixed) without a schema change.
- Constant-time comparison (`crypto/subtle`) for the derived key.

### D3 — Throttling & lockout *(owner decision: "Ketat")*
Three layers, outermost first:

| Layer | Key | Limit | Store |
|---|---|---|---|
| Nginx | `$binary_remote_addr` on `/api/v1/auth/staff/` | dedicated `auth_limit` zone, 10 r/min, burst 5 | Nginx shared memory |
| App per-IP | trusted client IP (requires F2 fix) | 20 failed attempts / 15 min sliding window | in-process (single VPS per ADR-002) |
| Account lockout | `(store_id, user_id)` | **5 consecutive failures → locked 15 min** | persisted on `users` (`failed_pin_attempts`, `locked_until`) |

- Lockout state is read and updated in the same transaction with `SELECT … FOR UPDATE` on the user row, so concurrent wrong-PIN attempts cannot overshoot the threshold.
- A successful login resets `failed_pin_attempts` to 0.
- The response for a locked account is the **same generic `401 INVALID_CREDENTIALS`** as a wrong PIN. Lock state is visible to the owner in backoffice (owner can unlock early, audited as `STAFF_UNLOCK`).
- Entering the locked state writes `audit_logs` action `STAFF_LOGIN_LOCKED` (actor = locked user, reason = system-generated).
- **Accepted risk — targeted lockout DoS:** anyone who knows a staff email and store slug can keep that account locked. Mitigations: per-IP limits, owner unlock, 15-minute window. Revisit with device binding if abused.

### D4 — Session lifecycle *(owner decision: "Ketat")*

| Property | Value |
|---|---|
| Access token | JWT HS256, **TTL 15 min**, sent as `Authorization: Bearer` |
| Refresh token | opaque, 256-bit `crypto/rand`, base64url; **absolute session lifetime 12 h** (one shift), rotated on every refresh |
| Server state | `auth_sessions` row per login; only **SHA-256** of refresh tokens is stored |
| Reuse detection | presenting an already-rotated refresh token revokes the whole session (`revoked_reason = 'refresh_reuse'`) and writes `STAFF_SESSION_REVOKED`. Within 30 s of a rotation the previous generation gets `409 REFRESH_RACE` instead (concurrent POS tabs), never new tokens |
| Multi-device | allowed; **max 3 active sessions per user** (oldest revoked on overflow), **idle timeout 2 h** — *owner decision O3* |
| Logout | revokes the current session; access token dies within ≤15 min and is rejected sooner by D4 revalidation |
| Forced revocation | deactivation, PIN reset, role change → revoke **all** sessions of that user in the same transaction |

**Access-token claims** (replaces `CustomClaims` for issued tokens):

| Claim | Value |
|---|---|
| header `kid` | key ID from a key ring (`STAFF_JWT_KEYS`), enabling rotation without a flag day; falls back to `JWT_SECRET_KEY` as `kid=k1` during migration |
| `iss` | `mycaffe-backend` (unchanged) |
| `aud` | `mycaffe-pos` (unchanged; reserved for staff only — guest/QR tokens get different audiences) |
| `typ` | `staff_access` (new; validator rejects any other purpose) |
| `sub` | `users.id` |
| `store_id`, `role` | as today |
| `sid` | `auth_sessions.id` |
| `jti`, `iat`, `exp` | unique ID, issued-at, expiry |

**Per-request revalidation:** authentication middleware, inside `WithTenantTx(store_id)`, loads `auth_sessions` + `users` by `(store_id, sid, sub)` and rejects if the session is revoked/expired, the user is inactive or locked, or `users.role` differs from the `role` claim. The **database role is authoritative**; a stale claim yields `401 SESSION_STALE` and the client refreshes. Cost: one indexed lookup per request — acceptable at 100–500 orders/day/store (ADR-002).

**Refresh-token transport:** HttpOnly cookie `__Secure-mc_staff_rt`, `Secure`, `SameSite=Strict`, `Path=/api/v1/auth/staff`. Refresh and logout additionally require header `X-CSRF-Protection: 1` (forces a CORS preflight, blocking cross-site form posts). Access tokens are kept in client memory only, never `localStorage`. *Frontend and API share one registrable domain — owner decision O2.*

**Config changes:** `STAFF_ACCESS_TOKEN_TTL=15m`, `STAFF_REFRESH_TOKEN_TTL=12h`, `STAFF_MAX_SESSIONS_PER_USER=3`, `STAFF_SESSION_IDLE_TIMEOUT=2h`, `STAFF_PIN_MAX_FAILURES=5`, `STAFF_PIN_LOCKOUT=15m`, `STAFF_JWT_KEYS` (optional key ring). `JWT_EXPIRED_HOURS` is ignored once 02.01-B ships and is removed in a later release. Production validation fails closed if TTLs exceed 1 h (access) / 24 h (refresh).

### D5 — Pre-authentication tenant resolution
- New function `resolve_store_for_login(p_slug VARCHAR) RETURNS TABLE (store_id UUID, is_active BOOLEAN)`, `SECURITY DEFINER`, `SET search_path = public, pg_temp`, owned by `mycaffe_migrator`, `EXECUTE` granted to `mycaffe_app` only, `REVOKE … FROM PUBLIC`.
- Returns at most one row, only where `deleted_at IS NULL`; exposes no name, settings, or other columns.
- All user lookups then run as `mycaffe_app` inside `WithTenantTx(store_id)` under normal RLS. No other RLS bypass is introduced.
- This follows the existing pattern of `resolve_payment_webhook_context` and `list_stores_with_pending_reconciliation` and must be added to the allow-list in AGENTS.md when implemented.

### D6 — Sensitive-action PIN re-authentication *(owner decision O1, 2026-10-31)*
The design review flagged a conflict between Module 3 ("cashier must not void/refund without the configured authorization flow") and the brand guide ("cashier can void/refund with mandatory reason"). `docs/architecture/decisions-before-code.md` §6 (already approved) resolves it: *"Sensitive cashier actions require a PIN and mandatory reason logged to append-only `audit_logs`."* Proposed contract:
- Void/refund/stock override/price override/shift-close discrepancy require the **acting user to re-enter their own PIN** plus a non-empty reason in the same request.
- No owner approval step, no second actor (preserves the "no multi-step approval" rule).
- Re-auth PIN failures count toward the same D3 lockout counter.
- Void/refund above a per-store amount threshold (`void_refund_owner_threshold`, default Rp100.000) requires the **owner PIN** instead of the acting cashier PIN (owner decision O1). Discounts above `cashier_max_discount_bps` (default 10%) and every stock override also require the owner PIN (O4).

### D7 — RBAC: single permission policy, four fixed roles
- Roles stay exactly `owner`, `cashier`, `barista`, `kitchen`. No new role, no per-store custom roles in Phase 02.
- One Go package (`internal/pkg/rbac`) defines permission constants and a static `role → permission set` map. Handlers declare a required permission via route middleware; services that perform critical operations re-check the permission against the trusted actor (defence in depth).
- The domain role enum, JWT role validation, and DB `CHECK` stay in lock-step via a unit test that compares all three.
- **Unresolved powers default to owner-only (fail-closed)** until the owner decides otherwise.

| Permission | owner | cashier | barista | kitchen | Note |
|---|:-:|:-:|:-:|:-:|---|
| `store.settings.manage` | ✓ | | | | |
| `staff.manage` (create, deactivate, role change, PIN reset, unlock) | ✓ | | | | last active owner cannot be deactivated or demoted |
| `catalog.manage` | ✓ | | | | |
| `catalog.cost.read` (HPP, margins) | ✓ | | | | |
| `report.finance.read` | ✓ | | | | |
| `order.pos.create` | ✓ | ✓ | | | |
| `order.read.store` | ✓ | ✓ | | | |
| `payment.tender.direct` (cash, static QRIS, EDC) | ✓ | ✓ | | | today enforced against the client-supplied actor in `payment_repository.go` (`role IN ('owner','cashier')`); 02.01-C switches it to the trusted actor |
| `payment.initiate.gateway` (dynamic QRIS, e-wallet, VA) | ✓ | ✓ | | | |
| `tab.manage`, `split_bill.manage` | ✓ | ✓ | | | |
| `shift.open`, `shift.close` | ✓ | ✓ | | | close with discrepancy → D6 PIN + reason |
| `order.void` | ✓ | ✓ (D6; owner PIN above threshold) | | | O1 approved |
| `payment.refund` | ✓ | ✓ (D6; owner PIN above threshold) | | | O1 approved |
| `order.discount.apply` | ✓ | ✓ up to `cashier_max_discount_bps` (default 10%) + reason; above → owner PIN | | | O4 approved |
| `stock.override` | ✓ | owner PIN required | | | O4 approved |
| `kds.ticket.read.barista` / `.update.barista` | ✓ | read | ✓ | | |
| `kds.ticket.read.kitchen` / `.update.kitchen` | ✓ | read | | ✓ | |
| `item.availability.toggle.beverage` | ✓ | | ✓ | | |
| `item.availability.toggle.food` | ✓ | | | ✓ | |
| `table.manage`, `table.qr.manage` | ✓ | | | | 02.01-D |

Cross-tenant access is impossible for every role by construction (RLS + trusted `store_id`), not by a permission.

### D8 — Route authentication classes
Every route gets exactly one class; a route accepting more than one token type is forbidden.

| Class | Credential | Routes |
|---|---|---|
| `public` | none | `/health*`, `GET /api/v1/public/stores/{slug}/catalog` (planned) |
| `public-auth-exchange` | credential in POST body, no-store response, rate-limited | `POST /api/v1/auth/staff/login`, `POST /api/v1/public/table-sessions` (02.01-E) |
| `staff-refresh` | refresh cookie + `X-CSRF-Protection` | `POST /api/v1/auth/staff/refresh`, `POST /api/v1/auth/staff/logout` |
| `staff` | access token + permission | `GET /api/v1/auth/staff/me`, `/api/v1/staff/*`, `/api/v1/pos/*`, `/api/v1/tables/*` |
| `guest` | guest table-session token | `/api/v1/public/orders/*` (02.01-E) |
| `provider-webhook` | provider signature | `POST /api/v1/payments/midtrans/webhook` (unchanged) |
| `ws-ticket` | one-time ticket | `/ws` (Phase 03; stays contained) |

Consequence for current routes (02.01-C): `POST /api/v1/orders` and `POST /api/v1/orders/:id/payments/initiate` move under `staff` (POS path) and lose `store_id` / `actor_user_id` from their request bodies — a **breaking contract change**, approved as part of this ADR. The guest ordering path is a separate route in 02.01-E. Both stay contained in staging/production until their middleware and integration tests land.

## 3. Threat model (STRIDE, staff-auth surface)

| Threat | Vector | Control | Test (02.01-B/C) |
|---|---|---|---|
| **S**poofing — PIN brute force | repeated login | D3 three-layer limits + lockout; F2 fix | 5 failures lock; 6th correct PIN still `401`; concurrent failures do not exceed threshold |
| Spoofing — account enumeration | response/timing differences | generic `401`, dummy argon2id | identical body/status for unknown store, unknown email, inactive, locked, wrong PIN |
| Spoofing — IP spoofing | forged `X-Forwarded-For` | trusted proxies = Nginx only (F2) | header from untrusted peer is ignored |
| Spoofing — token forgery / alg confusion | `alg=none`, RS/HS swap | existing HS256-only check + `kid` allow-list | unknown `kid`, `alg=none`, wrong `typ` rejected |
| Spoofing — cross-domain token reuse | guest/QR token sent to staff route | distinct `aud` + `typ` per domain | guest/QR token on staff route → `401` |
| **T**ampering — client-supplied identity | `store_id`, `actor_user_id`, `role` in body | removed from contracts; derived from middleware | body `store_id` of tenant B is ignored/rejected |
| Tampering — CSRF on refresh/logout | cross-site POST with cookie | `SameSite=Strict` + required custom header | request without header → `403` |
| **R**epudiation | denied sensitive action | `audit_logs` with actor from trusted context; D6 reason | every D6 action writes one audit row |
| **I**nformation disclosure — token leakage | logs, query strings, `localStorage` | no tokens in URLs (F3), redaction in logger, memory-only access token, `Cache-Control: no-store` | log capture contains no `Authorization`, PIN, or refresh token |
| Information disclosure — refresh-token DB theft | DB dump | SHA-256 hash only | stored value ≠ issued token |
| **D**enial of service — argon2id exhaustion | login flood | Nginx zone, per-IP limit, hash semaphore → `429` | >N concurrent logins get `429` without hashing |
| DoS — targeted lockout | wrong PINs for a known email | accepted risk (D3) | owner unlock clears lock |
| **E**levation — stale role | role demoted mid-session | DB role authoritative per request (D4) | demoted cashier's next request → `401 SESSION_STALE` |
| Elevation — refresh replay | stolen rotated refresh token | reuse detection revokes session | replayed token revokes session for both holders |
| Elevation — last owner removal | owner deactivates/demotes self | invariant check in tx | `409 LAST_OWNER` |
| Elevation — cross-tenant ID substitution | object ID of tenant B | RLS + trusted `store_id` | integration test as `mycaffe_app` returns 404 |

## 4. Schema delta planned for 02.01-B (not implemented here)
Migration `20261101000005_add_staff_auth_sessions` (additive, reversible):
1. Pre-check: fail the migration with a clear message if `(store_id, lower(email))` duplicates exist; then `UPDATE users SET email = lower(trim(email))` and `CREATE UNIQUE INDEX uq_users_store_email_ci ON users (store_id, lower(email))`.
2. `users`: add `failed_pin_attempts INT NOT NULL DEFAULT 0 CHECK (>= 0)`, `locked_until TIMESTAMPTZ`, `pin_changed_at TIMESTAMPTZ`.
3. `auth_sessions`: `id, store_id, user_id, created_at, expires_at, last_refreshed_at, revoked_at, revoked_reason, user_agent_hash, created_ip`; composite FK `(store_id, user_id) → users(store_id, id) ON DELETE CASCADE`; `UNIQUE(store_id, id)`; RLS enabled with the standard `store_isolation_*` policy; index `(store_id, user_id) WHERE revoked_at IS NULL`.
4. `auth_refresh_tokens`: `id, store_id, session_id, token_hash BYTEA UNIQUE, issued_at, expires_at, rotated_at`; composite FK `(store_id, session_id) → auth_sessions(store_id, id) ON DELETE CASCADE`; RLS enabled.
5. `resolve_store_for_login(p_slug)` per D5. The refresh path resolves the tenant from the token hash with a second narrowly-scoped resolver `resolve_refresh_token_context(p_hash BYTEA)` returning only `(store_id, session_id)`.
6. Extend `chk_audit_logs_action` with `STAFF_LOGIN_LOCKED`, `STAFF_UNLOCK`, `STAFF_PIN_RESET`, `STAFF_DEACTIVATED`, `STAFF_SESSION_REVOKED`.
7. `scripts/verify/verify_schema_constraints.sql` and CI migration job extended for the new tables, RLS flags, function ownership, and grants.

Down migration drops the new objects and columns, restores the previous audit CHECK, and keeps the email normalization (data change is not reverted).

## 5. Implementation sequence after approval

| Increment | Contents | Exit gate |
|---|---|---|
| 02.01-B | F2 trusted proxies; argon2id PIN package; migration 000005; login/refresh/logout/me handlers & service; token issuer with `kid`/`typ`/`sid`; config; dev seed hashes | unit + PG18 integration tests as `mycaffe_app` for every row of §3 covering login/session; 3 CI gates green |
| 02.01-C | auth middleware with per-request revalidation; `internal/pkg/rbac`; trusted actor context; remove `store_id`/`actor_user_id` from bodies; owner staff-management endpoints; F3 query-token removal | table-driven role × permission tests; cross-tenant denial; contained routes still `503` in staging/prod |
| 02.01-D/E | tables, QR credentials, guest sessions (separate ADR-004) | per design review §10 |

## 6. Logging rules for this surface
- Never log PIN, refresh token, access token, `Authorization` header, or `Set-Cookie`.
- Failed-login log line: `request_id`, trusted client IP, `store_slug` (already public in QR URLs), a truncated SHA-256 of the normalized email (for correlation without storing the address), and the outcome category (`invalid_credentials`, `rate_limited`). The internal reason (unknown email vs wrong PIN vs locked) is logged server-side only, never returned.

## 7. Owner decisions (resolved 2026-10-31)

| # | Decision |
|---|---|
| O1 | D6 confirmed: cashier own PIN + reason; above `void_refund_owner_threshold` (default Rp100.000) owner PIN required |
| O2 | POS frontend and API share one registrable domain; refresh via HttpOnly cookie |
| O3 | Max 3 active sessions per staff, idle timeout 2 h, absolute 12 h |
| O4 | Cashier discount up to `cashier_max_discount_bps` (default 10%) with reason; above that and every stock override need owner PIN |
| O5–O8 | Printed QR: no time expiry, version rotation + per-store emergency pause. Guest: one session per device, 3 h. Recorded in `docs/architecture/decision-register.md`; detailed in ADR-004 (02.01-D/E) |

## 8. Rollback
This ADR is documentation only. Each later increment is independently reversible: migration 000005 has a tested down migration; new routes are additive; the contained routes remain `503` in staging/production until 02.01-C passes, so reverting 02.01-B/C restores today's behaviour.
