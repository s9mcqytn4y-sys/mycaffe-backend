# Roadmap, Iterations & Checklist

Reviewed 2026-11-07 against `staging` through migration **000051** (v0.8 money & operations, v0.9 cafe floor,
v0.10 per-device offline sync, v0.11 promotions/bundles/inventory/purchasing/sales-channels, and v0.12
multi-tenant SaaS governance & BYOK). Master QR Table Order release plan:
[ReleasePlanQrTableOrderV1.md](../Product/ReleasePlanQrTableOrderV1.md); competitor benchmarks:
[CompetitorAnalysisMajoo.md](../Product/CompetitorAnalysisMajoo.md) and
[CompetitorAnalysisInterActive.md](../Product/CompetitorAnalysisInterActive.md); SaaS governance ADR:
[Adr009MultiTenantSaasGovernanceAndByok.md](Adr009MultiTenantSaasGovernanceAndByok.md). Status uses the four-tier classification from `AGENTS.md`; a module counts as done
only when its route, authorization, tests, and docs exist. Audit tickets and their status:
[AuditBacklog.md](../Engineering/AuditBacklog.md).

**Owner direction (2026-11-04, V1–V15): the freeze is lifted.** Release train v0.8 money & operations →
v0.9 cafe floor → v0.10 per-device offline → v0.11 channels & customers → v0.12 services; the
prospective-client demo waits for v0.8. Benchmark: [CompetitorAnalysisMajoo.md](../Product/CompetitorAnalysisMajoo.md).

## v0.8 plan (one PR per row, merged into `staging` on green CI per V3)

| PR | Scope | Decisions | Notes |
|----|-------|-----------|-------|
| 8.1 | Outbox dead-letter, attempts, backoff, purge of technical tables (P0-08) | — | **Done** (PR #59, migration 000025) |
| 8.2 | Messaging: `message_outbox` + sender interface (`log`, SMS aggregator), `otp_challenges`, `customers` with verified phone, staff PIN reset by SMS; demo owner e-mail back to the owner's Gmail | V4, V5, V6 | **Done** (migration 000026): drivers `log`/`disabled`; the provider driver waits for the owner to pick a vendor; WhatsApp waits for a domain (V8) |
| 8.3 | Multi-payment (direct methods free, dynamic QRIS last), split by amount, cash rounding | V11, V12 | **Done** (migration 000027). Split by amount is covered by partial tenders (each payer pays an amount on the same split), so no separate split mode |
| 8.4 | Outlet cash fund, expense categories, daily limit with `outlet_expense` approval, cashier settings (default float, blind count) | V13 | **Done** (migration 000028): fixed categories (ingredients, operational, other) instead of a category table; ADR-007 action `outlet_expense` |
| 8.5 | Partial refunds per item (proportional tax/service/discount), outlet cash as a refund source, RF1-a/b/d/f Midtrans refunds with `refund_pending` | RF1, V13 | Split in two. **8.5a done** (migration 000029): partial refunds by lines, outlet cash source, hand-back of a tender on a partly paid bill, idempotent line refunds. **8.5b** Midtrans API refunds with `refund_pending` follows; a live sandbox proof needs the owner's sandbox keys |
| 8.6 | Discount approval above `cashier_max_discount_bps` (ADR-007 action `discount`), compliment as an approved 100 % discount | V12 | **Done** (migration 000031): `POST /pos/orders/:id/discount`, ADR-007 action `discount`, zero-total splits allowed for compliments |
| 8.7 | Staff QR badges (revocable token) + PIN login | V14 | **Done** (migration 000032): `POST /staff/:id/badge`, `/badge/revoke`, login with `badge` + `pin` |
| 8.8 | P0-07 configuration (real client IP, Named Tunnel template, firewall runbook) and P0-09 provisioning/incident runbook | V8 | **Done**: prod Named Tunnel ingress (no published ports, Certbot removed), [OperationsRunbook.md](../Setup/OperationsRunbook.md) |

Blocked by dependency: Web Push, WhatsApp sending (Meta business verification).

## v0.9 plan: cafe floor ([PrdV09CafeFloor.md](../Product/PrdV09CafeFloor.md), decisions W5–W10)

| PR | Scope | Decisions | Notes |
|----|-------|-----------|-------|
| 9.1 | Move, merge, split tables; area filter on `tables.section` | W5 | **Done** (migration 000033 `table_session_links`) |
| 9.2 | Table timer, warnings only, extend with audit | W6 | **Done** (migration 000034: timer columns on `table_sessions`, `list_stores_with_due_table_timers`) |
| 9.3 | Queue TV with a per-store display token, WS channel `queue` | W7 | **Done** (migration 000035 `display_tokens`; the token carries the store id, so no new `SECURITY DEFINER`) |
| 9.4 | Print job queue per station, checker, TSPL labels, print agent API | W8 | **Done** (migration 000036 `printers`, `print_jobs`; purge after 30 days) |
| 9.5 | Digital receipts: e-mail, WhatsApp Cloud API driver, SMS link | W9 | **Done** (migration 000037 `receipt_deliveries`; WhatsApp waits for the Meta token) |

Analytics for these events: [AnalyticsPlan.md](../Product/AnalyticsPlan.md). Demo host: WSL Ubuntu behind the `mycaffe-demo` Named Tunnel at `demo.abbdullaziz.online` (W2).

**Schema (37 tables, 24 migrations):** approval_requests (v0.7); receipt_tokens, media_uploads, payment_refunds (v0.6.0); stores, users, auth_sessions, account_tokens, email_outbox,
store_settings, audit_logs; categories, products, product_variants, modifier_groups, modifier_options,
product_modifier_groups, option_modifier_groups; store_daily_sequences, orders, order_items,
order_item_options, order_rounds, payment_splits, payment_split_items, payments,
payment_notification_logs; shifts, cash_drawers, cash_movements; tables, table_sessions, guest_sessions;
outbox_events, store_event_sequences, ws_tickets; kds_tickets.

**Live HTTP surface:** health (live/ready); staff auth (login/refresh/logout/me, PIN forgot·setup·change);
staff management; store settings; catalog read (staff + public, ETag) and catalog CRUD incl. modifiers;
POS quote, orders, payments, void, refund, stock override; shifts, drawers, cash movements, X/Z reports;
tabs, rounds, split bill; tables, table sessions, QR rotate; guest QR session, guest orders and payments,
public order tracker; KDS tickets and transitions; WebSocket `GET /ws` with single-use tickets (staff and
guest); Midtrans webhook. Metrics on the internal `METRICS_ADDR` only.

## Done

| Area | Status | Evidence |
| ------ | -------- | ---------- |
| 01 Database runtime, triple-role RLS, migrator | Implemented & Tested | migrations 000001–000003, verify script |
| 01 Pricing engine, transactional orders, stock reservation | Implemented & Tested | `pricing.go`, order integration tests |
| 01.04 Payment ledger, webhook inbox, reconciliation, duplicate settlement | Implemented & Tested | migration 000004, payment integration tests |
| 02.01-A/B Auth design, staff login, rotating sessions, lockout, operator CLI | Implemented & Tested | migration 000005, ADR-003, auth tests |
| 02 Email outbox, invite/reset PIN, 6-digit PIN | Implemented & Tested | migration 000006, lifecycle tests |
| 02.01-C RBAC, store settings, token-bound POS routes | Implemented & Tested | migration 000007, RBAC HTTP tests |
| 3 Catalog C1–C3, variants, standard error contract, R2 buckets provisioned | Implemented & Tested | migration 000008, [ApiConventions.md](ApiConventions.md) |
| 4a Modifier groups/options CRUD, quote API (P8) | Implemented & Tested | iteration 4 design |
| 4b Shift & cash drawer, X/Z report (P6, P7, C1, H1) | Implemented & Tested | [Iteration4bShiftDrawerDesign.md](../Archive/Iteration4bShiftDrawerDesign.md) |
| 4c Open tab, rounds, split bill (P2, P3, P10–P14) | Implemented & Tested | [Iteration4c2SplitBillPlan.md](../Archive/Iteration4c2SplitBillPlan.md) |
| 4d Void / refund, hotfix v0.4.3 | Implemented & Tested | [Iteration4dVoidRefundStockPlan.md](../Archive/Iteration4dVoidRefundStockPlan.md); review follow-ups F1–F3 open (PR A) |
| ADR-004 Catalog cleanup (variant-only pricing) | Implemented & Tested | [Adr004CatalogSchemaModernization.md](Adr004CatalogSchemaModernization.md) |
| 5a Tables, table sessions, QR resolve | Implemented & Tested | migration 000016 |
| 5b Guest session, QR pay-first orders, tracker | Implemented & Tested | migration 000017 |
| 5c Outbox relay → WebSocket, WS tickets | Implemented & Tested | migration 000018 |
| 5d KDS tickets, late-payment flag (Q7) | Implemented & Tested | migration 000019; Q7 integration test open (P1-12) |
| Audit P0-01..P0-06: CI integration tests, backups, logs/metrics, alerting, deploy by digest, supply chain | Implemented & Tested | PR #38–#43 |

## Go-live plan (ordered; each PR waits for owner approval before merge; steps 0–B4 released in v0.6.0)

| Step | Item | Acceptance criteria | Ticket |
| ------ | ------ | --------------------- | -------- |
| 0 | **Housekeeping**: this roadmap, [AuditBacklog.md](../Engineering/AuditBacklog.md); then promote `staging` → `main` | docs match the code | P0-12 (part) |
| A | **Review debt v0.5.2**: F1 shop-wide owner-PIN guess limit (HF5), F2 PIN policy from config, F3 by-item refund stock release/waste, WebSocket revocation + connection caps, at-least-once delivery docs | named tests per item | 4d review |
| B1 | **Receipts** (R1): structured payload, ESC/POS 58/80 mm, 90-day digital link, void/refund receipts | snapshot-only, no recalculation; implemented (migration 000020) | P0-11 |
| B2 | **Owner reports**: per business day (store time zone), payment method, category/item, hour, void/refund, discount, tax & service; date range; CSV | consistent with Z-report; read-only with indexes; implemented (migration 000021, calendar day until PD5 cutoff) | P1-02 |
| B3 | **Media C4**: presigned PUT to `mycaffe-media`, magic bytes, size, EXIF strip, JPEG variants (K9 deviation) | `product.image_url` only from verified objects; implemented (migration 000022) | R8 |
| B4 | **RF1 staged**: RF1-c manual cash/transfer, RF1-e window per role (implemented, migration 000023); RF1-f `refund_pending` moves with RF1-a/b Midtrans refund, a separate PR after sandbox test | per decision register RF1 | P1-01 |

Previously frozen items now **Implemented & Tested**:
- **v0.10 Per-Device Offline Cash Sync (ADR-005)**: device registration/revocation, offline PIN verifier, versioned catalog snapshot, `/api/v1/device/offline-orders/sync`, owner conflict review queue (migrations 000038-000041).
- **v0.11 Promotions, Bundles, Inventory & Delivery Channels**: automatic best line promotion, product bundles, ingredients & recipes with automatic COGS deduction, suppliers & staged purchase orders, payables & instalments, stock count/waste approval, and POS delivery sales channels (migrations 000042-000049).
- **v0.12 Multi-Tenant SaaS Governance, Dual-Lane BYOK Midtrans & Self-Maintain Topology (ADR-009)**: legal documents & UU PDP consent, self-service tenant onboarding, store KYB profiles, Lane 1 platform subscription invoices vs Lane 2 AES-256-GCM BYOK Midtrans credentials + per-store webhook verification, storefront CMS & `id-ID`/`en-US` i18n, custom SMTP/WhatsApp configs, self-maintain LAN/ESC-POS hardware nodes & visual network simulator, tax-safe account clearance, and AzureDevID Superadmin console (migration 000051).

## Production readiness (prepare now, cut over when the client buys VPS)

| Item | Status |
| ------ | -------- |
| CI runs every integration test on PostgreSQL 18 (P0-01) | Implemented & Tested |
| Immutable GHCR images with provenance + SBOM, Trivy gate, GitHub Release on `v*` tags (P0-06) | Implemented & Tested |
| Deploy by digest with pre-deploy backup and automatic rollback (P0-05) | Implemented; first VPS run pending |
| Hourly encrypted R2 backups, retention tiers, `mycaffe_backup` role, restore drill script (P0-02) | Implemented; drill on first production VPS pending |
| JSON logs with request id, internal Prometheus metrics, Sentry Free Plan PII-scrubbed capture (P0-03, R6) | Implemented & Tested |
| Grafana Cloud metrics and tested alert rules (P0-04) | Implemented; Grafana Cloud account pending (owner) |
| Nginx TLS bootstrap, HSTS, request id, rate limits; internal-only DB network | Implemented & Tested |
| Domain `abbdullaziz.online`, Cloudflare DNS/WAF, Named Tunnel `mycaffe-demo` (`demo.abbdullaziz.online`), R2 CDN (`cdn.abbdullaziz.online`) | Implemented & Tested |
| Outbox dead-letter and purge (P0-08) | Implemented & Tested (v0.8 PR 8.1) |
| Provisioning and incident runbook (P0-09) | Implemented ([OperationsRunbook.md](../Setup/OperationsRunbook.md)) |
| Dual-Lane Midtrans Sandbox Core API + Tenant BYOK AES-256-GCM + Static QRIS Fallback | Implemented & Tested (ADR-009) |
| Unified Client Monorepo (`myCaffe-client`: Web PWA, Android Jetpack Compose ESC/POS, iOS Obj-C Bridge + Xcode CI) | Implemented & Tested |

## Open external dependencies (owner)

1. Client production Hostinger KVM 2 VPS provisioning -> triggers first immutable GHCR production deployment and R2 restore drill.
2. Midtrans Production Business Review approval -> unblocks switching Lane 1 (`PLATFORM_MIDTRANS_IS_PRODUCTION=true`) from Sandbox to Production.
3. Komdigi PSE Lingkup Privat certificate via OSS RBA SSO PB-UMKU -> updates `pse_registration_no` on corporate legal footer.
