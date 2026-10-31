# Decision Register

Owner decisions that bind implementation. Recorded 2026-10-31 (structured interview with the owner).
Earlier decisions live in [decisions-before-code.md](decisions-before-code.md) and ADR-003
(PR #7, `docs/architecture/adr-003-staff-authentication-session-and-rbac.md`).

## Approved

| ID | Decision | Approved option | Implementation notes | Blocks |
|----|----------|-----------------|----------------------|--------|
| D1 | Staff login identity | `store_slug + email + PIN` | ADR-003 D1 | 02.01-B |
| D2 | PIN policy | 6–12 digits, weak-PIN denylist, argon2id `m=19456,t=2,p=1` | ADR-003 D2 | 02.01-B |
| D3 | Throttling & lockout | 5 consecutive failures → locked 15 min; generic `401` | ADR-003 D3 | 02.01-B |
| D4 | Token TTL | Access 15 min, refresh 12 h absolute, rotation + reuse detection | ADR-003 D4 | 02.01-B |
| O1 | Void / refund | Cashier re-enters **own PIN** + mandatory reason. Above a per-store amount threshold (default **Rp100.000**) the **owner PIN** is required. All attempts go to `audit_logs`. | New store setting `void_refund_owner_threshold` (int64 IDR). Supersedes ADR-003 D6 "out of scope" note on thresholds. | 02.01-C, POS void/refund |
| O2 | Production domains | POS frontend and API share **one registrable domain** (e.g. `pos.<domain>` + `api.<domain>`) | Refresh token in `__Secure-mc_staff_rt` HttpOnly cookie, `SameSite=Strict` (ADR-003). Domain name itself still to be purchased/assigned. | 02.01-B refresh transport |
| O3 | Staff sessions | Max **3** active sessions per staff (oldest revoked), **idle timeout 2 h**, absolute **12 h** | `STAFF_MAX_SESSIONS_PER_USER=3`, new `STAFF_SESSION_IDLE_TIMEOUT=2h` checked on refresh | 02.01-B |
| O4 | Discount & stock override | Cashier may discount up to a per-store max percent (default **10%**) with mandatory reason. Above that, and **every** stock override, require owner PIN. | New store setting `cashier_max_discount_bps` (default 1000). | POS discounts, inventory |
| Q5 | Printed table QR lifetime | **No time expiry.** Version-based revocation (`qr_version`) per table, signing-key rotation via key ID, plus a per-store **emergency pause** that disables all QR session creation instantly. | Dedicated QR signing key (never `JWT_SECRET_KEY`). | 02.01-D |
| Q6 | Guest sessions | **One session per device**; a guest sees only orders bound to its own session. TTL **3 h**, or ends when all of its orders are paid and completed. | Distinct `aud`/`typ` from staff tokens. | 02.01-E |
| Q7 | Late settlement after expiry | Keep current behavior: revive the order to the queue if stock allows, otherwise cancel + `payment.late_settlement_stock_exhausted`. **Add a "late payment" flag** visible on KDS and cashier screens. | Flag not implemented yet. | KDS (Phase 03) |
| Q8 | Demo / staging environment | Local Docker Compose stack + **Cloudflare Quick Tunnel**, auto-restart, `APP_ENV=staging`, Midtrans sandbox | [docs/setup/demo-staging-environment.md](../setup/demo-staging-environment.md) | — |

## Infrastructure & Phase 02.01-B (recorded 2026-10-31, second interview)

| ID | Decision | Approved option | Notes |
|----|----------|-----------------|-------|
| I1 | Role of the Windows laptop before a VPS exists | **Dev + demo only**, no real cafe transactions | Production waits for Hostinger KVM 2 + domain |
| I2 | How devices reach the demo | **Cloudflare Tunnel HTTPS only**; zero inbound ports, no LAN exposure | All compose ports bound to `127.0.0.1` |
| I3 | Origin layout in the demo | **One origin**: Nginx routes `/api`, `/ws`, `/health` to Go and everything else to Next.js on the host | Makes the `SameSite=Strict` refresh cookie (O2) testable without a domain |
| I4 | Developer workflow | **Go native on Windows + PostgreSQL in Docker**, driven by `scripts/dev.ps1` | No new tooling required |
| I5 | First store/owner in production | **Operator CLI** `cmd/migrate -action=bootstrap-store`, PIN via hidden prompt, audited `STORE_BOOTSTRAP` | No public sign-up endpoint |
| I6 | Owner PIN recovery | **Another owner resets it**; break-glass `-action=reset-owner-pin` (operator only) revokes all sessions and audits `STAFF_PIN_RESET` | No email/SMS dependency |
| I7 | 02.01-B implementation plan | **Approved** as one PR: migration 000005, resolver, login/refresh/logout/me, CLI, tests | Security boundary change approved by the owner |

Setup details: [docs/setup/windows-dev-environment.md](../setup/windows-dev-environment.md).

## Email, operations & hardware (recorded 2026-10-31, third interview)

| ID | Decision | Approved option |
|----|----------|-----------------|
| E1 | Login identity | **PIN only** for every role (D1 kept); **PIN is exactly 6 digits**; no passwords |
| E2 | Staff onboarding | Owner invites → staff sets own PIN via email link (48 h); "lupa PIN" link (30 min); owner temporary PIN (must change) for staff without email |
| E3 | Marketing email | Transactional/notification email only; marketing = consent capture + export, no campaigns via the Hostinger mailbox |
| E4 | Guest email | Optional email for the digital receipt; **separate, unchecked** marketing opt-in; consent stored append-only with text version and revocable (UU PDP) |
| E5 | Email delivery | PostgreSQL `email_outbox` written in the business transaction + in-process worker with retry; Gmail App Password for dev/demo, `smtp.hostinger.com:465` for production |
| T1 | Table occupancy | `table_session` opened by first scan/order; closed by cashier ("Kosongkan meja") or automatically 30 min (per store) after all orders are paid and served |
| T2 | Group order & split | All devices of a table join one table session; QR = pay-first per device; open tab only by cashier, split equally or by item |
| C1 | Shift & cash drawer | Opening float, blind count at close, variance > Rp5.000 (per store) needs reason + owner PIN, X/Z reports, recorded paid-outs; QRIS/EDC excluded from drawer |
| C2 | Refund | Recorded manual refunds (cash from the open drawer, non-cash by owner transfer), Midtrans refund API only where supported, partial per item, optional restock |
| C3 | Cashier doubling as barista | Per-store toggle grants the cashier role barista KDS permissions; no new role |
| H1 | Hardware | Chrome + ESC/POS thermal printer (USB via WebUSB/Web Serial, LAN/BT via local print bridge), drawer kick via printer, customer display via BroadcastChannel or WebSocket |
| R1 | Digital receipt | Random-token public page valid 90 days, emailed when an address is given and printed as QR; amounts from snapshots |
| N1 | Notifications | In-app WebSocket (toast/sound/KDS) from an outbox relay; owner email only for important events; web push deferred |
| S1 | Next PR | Email foundation + invitation/reset PIN (migration 000006) — implemented |

Full evaluation and ordered backlog: [pos-operations-evaluation.md](pos-operations-evaluation.md).

## Catalog, owner self-service & finance (recorded 2026-10-31, fourth interview)

| ID | Decision | Approved option |
|----|----------|-----------------|
| K1 | Menu variants | `product_variants` with own SKU, price and recipe (Regular/Large, Hot/Iced); modifiers stay for add-ons |
| K2 | Inventory depth | Ready-to-serve items counted in units; prepared items use recipes/BOM of ingredients (g/ml/pcs) deducted on payment, minimum stock alerts, moving-average cost for HPP, stock opname, append-only movements |
| K3 | Self-service configuration | All business settings editable by the owner in the app; infrastructure secrets (Midtrans keys, SMTP, DB, DNS) stay in the server `.env` and only their status is shown |
| K4 | Subscriptions | Hostinger auto-renew + in-app expiry dates (domain/VPS/email), automatic TLS expiry check, owner email at 30/7/1 days and on failed backup |
| K5 | Midtrans settlement | No withdraw button exists; app shows expected daily settlement (gross, MDR, net) and reconciles against an imported Midtrans settlement CSV |
| K6 | Loyalty | Digital stamp card ("buy N get 1"), customer identified by a verified email, one stamp per paid order, reward = audited 100% item discount |
| K7 | Promo & bundling | Owner-defined automatic promos (percent/amount, product/category, happy hour, period, quota), fixed-price bundles, cashier discount within O4; one best promo per line, no stacking; vouchers later |
| K8 | Static QRIS fallback | Automatic after 2 consecutive Midtrans failures or forced by the owner; cashier confirms with a reference; auto-return to dynamic QRIS after 10 minutes |
| K9 | Media storage | Cloudflare R2 via presigned upload; JPEG/PNG/WebP ≤ 5 MB, magic-byte check, EXIF stripped, WebP 200/600/1200 variants, CDN delivery |
| C-scope | Phase 02.01-C | Approved and implemented: RBAC matrix, `store_settings`, token-bound POS routes, `/me.permissions`, removal of client-supplied store/actor and of `/ws?token=` |

Designs: [catalog-inventory-design.md](catalog-inventory-design.md), requirements:
[../product/requirements-owner-staff-customer.md](../product/requirements-owner-staff-customer.md).

## Cleanup, single tenant & iteration 3 (recorded 2026-10-31, fifth interview)

| ID | Decision | Approved option |
|----|----------|-----------------|
| M1 | Docker cleanup | MyCaffe + safe garbage only (stopped containers, unused anonymous volumes, unused images, build cache); other projects' volumes and the MyCaffe demo/dev volumes kept |
| M2 | Supabase | Removed from the repo, local `.env`, local MCP config, global CLI and `~/.supabase`; the Claude account connector is disconnected by the owner |
| M3 | Single-tenant MVP | Multi-tenant schema kept for future branches; optional `DEFAULT_STORE_SLUG` so staff log in with email + PIN only; demo seed keeps two stores for isolation tests; demo owner email `abdulaziz543333@gmail.com` |
| M4 | Iteration 3 priority | Catalog C1–C3 with the standard CRUD contract; media upload after the R2 bucket exists |

## Resolved from existing sources (not re-asked)

| Topic | Resolution | Source |
|-------|-----------|--------|
| Split bill semantics | Deferred until the single-payer path is stable; equal vs item split, rounding and partial refunds to be specified then. Code currently assumes **one split per order** (`commitPaidOrderTx` settles all splits). | `docs/product/module-1-customer-ordering.md` |
| Roles | Exactly `owner`, `cashier`, `barista`, `kitchen`; no new roles. | `AGENTS.md`, migration `000001` |

## Still open

| Topic | Needed for | Default until decided |
|-------|-----------|-----------------------|
| Production domain name & DNS | Named Cloudflare Tunnel / TLS, cookie domain (O2) | Quick Tunnel URL for demo only |
| Midtrans production credentials & VPS provisioning | Production cutover | Sandbox only |
| WebSocket ticket lifecycle (TTL, single use) | Phase 03 realtime | `/ws` stays `503` in staging/production |
