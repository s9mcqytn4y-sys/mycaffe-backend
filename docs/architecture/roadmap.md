# Roadmap, Iterations & Checklist

Reviewed 2026-10-31 (iteration 3). Status uses the four-tier classification from `AGENTS.md`; a
module counts as done only when its route, authorization, tests, and docs exist. MVP scope: **one
cafe (single tenant)**, multi-tenant schema kept for future branches (owner decision M3).

**Schema (23 tables):** stores, users, auth_sessions, account_tokens, email_outbox, store_settings,
categories, products, product_variants, modifier_groups, modifier_options, product_modifier_groups,
option_modifier_groups, store_daily_sequences, orders, order_items, order_item_options, payment_splits,
payment_split_items, payments, payment_notification_logs, outbox_events, audit_logs.

**Live HTTP surface:** health; staff auth (login/refresh/logout/me/pin forgot·setup·change); staff
management; store settings; POS orders + payments; catalog read (staff + public, ETag) and catalog CRUD;
Midtrans webhook. Contained (503 in staging/production): guest `POST /api/v1/orders`, `GET /ws`.

## Done (iterations 1–3)

| Area | Status | Evidence |
| ------ | -------- | ---------- |
| 01 Database runtime, triple-role RLS, migrator | Implemented & Tested | migrations 000001–000003, verify script |
| 01 Pricing engine, transactional orders, stock reservation | Implemented & Tested | `pricing.go`, order integration tests |
| 01.04 Payment ledger, webhook inbox, reconciliation, duplicate settlement | Implemented & Tested | migration 000004, payment integration tests |
| 02.01-A/B Auth design, staff login, rotating sessions, lockout, operator CLI | Implemented & Tested | migration 000005, ADR-003, auth tests |
| 02 Email outbox, invite/reset PIN, 6-digit PIN | Implemented & Tested | migration 000006, lifecycle tests |
| 02.01-C RBAC, store settings, token-bound POS routes | Implemented & Tested | migration 000007, RBAC HTTP tests |
| Infra: trusted proxies, timeouts, request id, body cap, demo tunnel, dev.ps1 | Implemented & Tested | PR #8, #9, #13 |

## Iteration 3 checklist (this PR)

- [x] Supabase removed: repo MCP example, local `.env` (DB URL → local PostgreSQL, `SUPABASE_*` keys dropped), local MCP config, global CLI and `~/.supabase` (owner decision M2). Claude Supabase connector: owner to disconnect in Settings → Connectors.
- [x] Docker cleanup, MyCaffe + safe garbage only (M1): ~4.4 GB reclaimed; other projects' volumes kept.
- [x] Demo owner account email → `abdulaziz543333@gmail.com` (seed upserts email).
- [x] Single-cafe login: optional `DEFAULT_STORE_SLUG` (M3).
- [x] Refactor: `payment_service.go` (1,539 lines) split into service/webhook/settlement/initiate/reconcile/helpers files, no logic change (29 functions before/after).
- [x] Catalog C1: staff + public catalog read model with ETag/304.
- [x] Catalog C2: migration 000008 — `product_variants` (SKU, price, default), product type, prep station, archive; order lines snapshot the variant; pricing uses the variant.
- [x] Catalog C3: CRUD categories/products/variants, station availability toggles, price-change audit, archive rules.
- [x] Standard CRUD/error contract (422 `fields`, 409 `field`) — [api-conventions.md](api-conventions.md).
- [x] OpenAPI: catalog paths and shared responses; Redocly lint clean.
- [x] Modifier group/option CRUD (C3b) + POS/Public Quote API + optimistic conflict (QUOTE_CHANGED) → Iteration 4a completed.
- [x] Cloudflare R2 Triple-Bucket Provisioning (`mycaffe-media`, `mycaffe-private-media`, `mycaffe-backups`) + CORS + backup/restore prefix bugfix → completed.
- [ ] Media upload handler / presigned PUT service (C4) → iteration 4b / 5.

## Next iterations (ordered; each item = one PR with migration, tests, OpenAPI, docs)

| Iter | Item | Acceptance criteria | Depends on / blocker |
| ------ | ------ | --------------------- | ---------------------- |
| 4a | **C3b Modifier groups & options CRUD** + product↔group links + Quote APIs | 422/409 contract, negative delta P1, nested sub-addon depth 1, cycle protection, POS quote RBAC (order.pos.create), quote fingerprint anti-compensating check (P8) | Implemented & Tested ✅ |
| 4b | **Shift & cash drawer (P6, P7, C1, H1)** | open with float, cash payments require an open shift, paid-out, blind close, variance > tolerance needs owner PIN, X/Z report, audit | store_settings ✅ |
| 4c | **Open tab, rounds, split bill (P2, P3, T2)** | split by equal N or items; server recomputes shares; deterministic rupiah rounding remainder to split 1 (equal N) or last split (by item); locked after first payment | 4a, 4b |
| 4d | **Void / refund (P4, P5, O1, C2)** + split rework (R4) | void before paid, refund after paid (cash from open drawer), owner PIN above threshold, `commitPaidOrderTx` settles one split | shift |
| 4 | **ADR-004 catalog cleanup** (done before iteration 5, owner 2026-11-01) | variant-only pricing, `single ⇒ max_select = 1` CHECK, unit stock stays on products | — |
| 5 | **5a Tables, table sessions, QR resolve** (plan: [iteration-5-table-qr-kds-plan.md](iteration-5-table-qr-kds-plan.md)) | tables CRUD, opaque rotatable QR (QR1), derived occupancy (T1), emergency pause honoured | QR1 + definer functions approved |
| 5 | **02.01-E Guest session + table session (T1, T2)** + optional email/consent (E4) | per-device guest token, pay-first QR orders, table occupancy states, auto-close | 02.01-D |
| 5 | **Outbox relay → WebSocket + WS ticket** (risk R2) | events delivered with replay by sequence, KDS/POS/TV channels, `/ws` opened | — |
| 6 | **KDS tickets** (station routing, preparing/ready/served, late-payment flag) | barista/kitchen permissions enforced, SLA timestamps | relay |
| 6 | **Receipts (R1)**: ESC/POS payload + digital receipt link + email | snapshot-only amounts, 90-day token | void/refund |
| 6 | **Inventory (C5)**: ingredients, recipes, movements, opname, low-stock email; HPP snapshot (C7) | gross profit only for recipe-backed items | variants ✅ |
| 7 | Promotions & bundles (K7), loyalty stamps (K6), static QRIS fallback flow (K8) | single best promo per line, snapshot on order | catalog |
| 7 | Reports: sales, shift, settlement estimate + CSV import (K5), subscription reminders (K4) | — | shift, payments |
| 7 | **C4 Media upload (R2)** | presigned PUT, magic bytes, EXIF strip, WebP variants | R2 bucket (owner) |

## Production readiness (prepare now, cut over when the client buys domain + VPS)

| Item | Status |
| ------ | -------- |
| Immutable GHCR images, prod compose with fail-fast required secrets | Implemented (CI) |
| Nginx TLS bootstrap, HSTS, request id, rate limits | Implemented |
| Edge subnet + `TRUSTED_PROXIES`, internal-only DB network | Implemented |
| Encrypted R2 backup/restore scripts | Implemented; **restore drill blocked (no VPS)** |
| Hostinger SMTP + SPF/DKIM/DMARC runbook | Documented ([email.md](../setup/email.md)); blocked (no domain) |
| Operator bootstrap (`bootstrap-store`) + `DEFAULT_STORE_SLUG` | Implemented |
| Sentry HTTP middleware (R6), metrics (R7), failed-email alert (R9) | Planned (iteration 5–6) |
| VPS hardening runbook (SSH key-only, ufw 22/80/443, unattended upgrades, docker log rotation) | Planned — write before cut-over |
| Promote `staging` → `main` (PR #10 pattern) after each iteration | Owner merges |

## Open blockers (owner)

1. Buy domain + Hostinger KVM 2 + mailbox → unblocks TLS, SMTP, restore drill, Named Tunnel.
2. Create a Cloudflare R2 bucket (media) → unblocks C4.
3. Midtrans production keys → unblocks live payments.
4. Disconnect the Supabase connector in Claude Settings → Connectors (account level).
