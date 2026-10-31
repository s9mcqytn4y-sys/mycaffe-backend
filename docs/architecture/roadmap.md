# Roadmap & Sub-task Status

Reviewed 2026-10-31 against `staging` (`19d2dfc` + this change). Status uses the four-tier
classification from `AGENTS.md`. A module only counts as done when its HTTP route, authorization,
tests and docs exist — a table or domain model alone is not an implementation.

**Schema today (18 tables):** `stores`, `users`, `categories`, `products`, `modifier_groups`,
`modifier_options`, `product_modifier_groups`, `option_modifier_groups`, `store_daily_sequences`,
`orders`, `order_items`, `order_item_options`, `payment_splits`, `payment_split_items`, `payments`,
`payment_notification_logs`, `outbox_events`, `audit_logs`.

**HTTP routes today:** `GET /health*`, `POST /api/v1/orders` (staging/prod `503`),
`POST /api/v1/orders/:id/payments/initiate` (staging/prod `503`),
`POST /api/v1/payments/midtrans/webhook`, `GET /ws` (staging/prod `503`).

## Phase 01 — Database, Orders, Payments

| Sub-task | Status | Evidence |
|----------|--------|----------|
| 01.01 Database runtime, triple-role RLS | Implemented & Tested | PR #2, migration `000001`, CI `database-migration-test` |
| 01.02 Pricing engine & sub-addons | Implemented & Tested | `internal/service/pricing.go` + tests |
| 01.03 Transactional order service | Implemented & Tested | PR #4, `order_service_integration_test.go` |
| 01.04 Payment ledger, webhook inbox, reconciliation | Implemented & Tested (service) / Guarded (HTTP) | PR #5, PR #8, `payment_service_integration_test.go` (6 scenarios incl. duplicate settlement) |

## Phase 02 — Authentication, RBAC, Signed QR (next)

All decisions are approved ([decision-register.md](decision-register.md)); nothing in Phase 02 is
blocked on the business any more. Order: B → C → D → E → F.

| Sub-task | Status | Scope | Acceptance criteria |
|----------|--------|-------|---------------------|
| 02.01-A Auth design (ADR-003, OpenAPI) | Design ready — PR #7 draft; update with O1–O4 | ADR, RBAC matrix, threat model | ADR reflects decision register; PR merged |
| 02.01-B Staff authentication | Planned | `auth_sessions` table (refresh hash, family, idle/absolute expiry), `resolve_store_for_login` SECURITY DEFINER, argon2id PIN, lockout columns on `users`, login/refresh/logout, case-insensitive email uniqueness, seed argon2id dev PINs | Login → 15 min access + rotating refresh cookie; reuse revokes family; 3-session cap; 2 h idle; lockout after 5; generic 401; PG18 tests incl. cross-tenant |
| 02.01-C Middleware & RBAC | Planned | Trusted actor/tenant from token; remove `store_id`/`actor_user_id` from bodies; permission policy; O1/O4 PIN re-auth + thresholds; audit; drop `?token=` on `/ws` | Every tenant route rejects missing/foreign token; cashier void above threshold needs owner PIN; tests per role |
| 02.01-D Tables & signed QR | Planned | `tables` (+ `qr_version`, `qr_ordering_enabled`), composite FKs, HMAC QR with key ID, rotate/revoke, per-store emergency pause, audit | Old QR version rejected; paused store rejects all scans; QR key ≠ JWT key |
| 02.01-E Guest table session | Planned | QR exchange → per-device guest token (3 h), order/session binding, own-order tracking | Guest A cannot read guest B's order on the same table; expired session rejected |
| 02.01-F Release readiness | Planned | Open `503` guards in staging, OpenAPI sync, RLS + tenant security review, staging verification on the demo stack | Order + payment flow works end-to-end through the tunnel with real auth |

## Phase 03 and later

| Phase | Module | Status | Main gaps |
|-------|--------|--------|-----------|
| 03 | POS & KDS realtime | Planned | Outbox relay → WebSocket, one-time WS ticket, station routing (barista/kitchen), `preparing/ready/served`, "late payment" flag (Q7), reconnect replay |
| 03 | TV queue display | Planned | Public read-only queue feed scoped by store, no PII |
| 04 | Catalog & inventory management | Planned (tables exist) | Product/category/modifier CRUD API, availability toggles, stock adjustment + movement history (no `stock_movements` table yet), stock opname, HPP/cost fields |
| 04 | Void / refund / discount | Planned | Refund ledger workflow (webhook currently only stores refund/chargeback events), O1 threshold, O4 discount cap |
| 05 | Shift & cash management | Planned (no tables) | `shifts`, cash drawer, open/close, discrepancy with PIN + reason |
| 05 | Expenses & finance | Planned (no tables) | Expense recording, gateway settlement & MDR reconciliation, gross profit (needs HPP), net profit (needs expenses) |
| 06 | Owner backoffice & reporting | Planned | Store settings API, staff management, sales/product reports, multi-outlet reports |
| 06 | Split bill | Deferred | Equal vs item split, partial payment; requires rework of `commitPaidOrderTx` (currently settles all splits) |
| 07 | Production readiness | Blocked by Dependency | Domain + DNS (O2), Midtrans production keys, Hostinger VPS, Named Tunnel or TLS, backup restore drill |
