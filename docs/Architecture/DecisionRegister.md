# Decision Register

Owner decisions that bind implementation. Recorded 2026-10-31 (structured interview with the owner).
Earlier decisions live in [DecisionsBeforeCode.md](../Archive/DecisionsBeforeCode.md) and ADR-003
(PR #7, `docs/Architecture/Adr003StaffAuthenticationSessionAndRbac.md`).

## Approved

| ID | Decision | Approved option | Implementation notes | Blocks |
| ---- | ---------- | ----------------- | ---------------------- | -------- |
| D1 | Staff login identity | `store_slug + email + PIN` | ADR-003 D1 | 02.01-B |
| D2 | PIN policy | 6–12 digits, weak-PIN denylist, argon2id `m=19456,t=2,p=1` | ADR-003 D2 | 02.01-B |
| D3 | Throttling & lockout | 5 consecutive failures → locked 15 min; generic `401` | ADR-003 D3 | 02.01-B |
| D4 | Token TTL | Access 15 min, refresh 12 h absolute, rotation + reuse detection | ADR-003 D4 | 02.01-B |
| O1 | Void / refund | Cashier re-enters **own PIN** + mandatory reason. Above a per-store amount threshold (default **Rp100.000**) the **owner PIN** is required. All attempts go to `audit_logs`. | New store setting `void_refund_owner_threshold` (int64 IDR). Supersedes ADR-003 D6 "out of scope" note on thresholds. | 02.01-C, POS void/refund |
| O2 | Production domains | POS frontend and API share **one registrable domain** (e.g. `pos.<domain>` + `api.<domain>`) | Refresh token in `__Secure-mc_staff_rt` HttpOnly cookie, `SameSite=Strict` (ADR-003). Domain name itself still to be purchased/assigned. | 02.01-B refresh transport |
| O3 | Staff sessions | Max **3** active sessions per staff (oldest revoked), **idle timeout 2 h**, absolute **12 h** | `STAFF_MAX_SESSIONS_PER_USER=3`, new `STAFF_SESSION_IDLE_TIMEOUT=2h` checked on refresh | 02.01-B |
| O4 | Discount & stock override | Cashier may discount up to a per-store max percent (default **10%**) with mandatory reason. Above that, and **every** stock override, require owner PIN. | New store setting `cashier_max_discount_bps` (default 1000). | POS discounts, inventory |
| Q5 | Printed table QR lifetime (**superseded by QR1**) | **No time expiry.** Version-based revocation (`qr_version`) per table, signing-key rotation via key ID, plus a per-store **emergency pause** that disables all QR session creation instantly. | Dedicated QR signing key (never `JWT_SECRET_KEY`). | 02.01-D |
| Q6 | Guest sessions | **One session per device**; a guest sees only orders bound to its own session. TTL **3 h**, or ends when all of its orders are paid and completed. | Distinct `aud`/`typ` from staff tokens. | 02.01-E |
| Q7 | Late settlement after expiry | Keep current behavior: revive the order to the queue if stock allows, otherwise cancel + `payment.late_settlement_stock_exhausted`. **Add a "late payment" flag** visible on KDS and cashier screens. | Implemented in 5d: `kds_tickets.late_payment`. | KDS (5d) |
| Q8 | Demo / staging environment | Local Docker Compose stack + **Cloudflare Quick Tunnel**, auto-restart, `APP_ENV=staging`, Midtrans sandbox | [docs/Setup/DemoStagingEnvironment.md](../Setup/DemoStagingEnvironment.md) | — |

## Infrastructure & Phase 02.01-B (recorded 2026-10-31, second interview)

| ID | Decision | Approved option | Notes |
| ---- | ---------- | ----------------- | ------- |
| I1 | Role of the Windows laptop before a VPS exists | **Dev + demo only**, no real cafe transactions | Production waits for Hostinger KVM 2 + domain |
| I2 | How devices reach the demo | **Cloudflare Tunnel HTTPS only**; zero inbound ports, no LAN exposure | All compose ports bound to `127.0.0.1` |
| I3 | Origin layout in the demo | **One origin**: Nginx routes `/api`, `/ws`, `/health` to Go and everything else to Next.js on the host | Makes the `SameSite=Strict` refresh cookie (O2) testable without a domain |
| I4 | Developer workflow | **Go native on Windows + PostgreSQL in Docker**, driven by `scripts/dev.ps1` | No new tooling required |
| I5 | First store/owner in production | **Operator CLI** `cmd/migrate -action=bootstrap-store`, PIN via hidden prompt, audited `STORE_BOOTSTRAP` | No public sign-up endpoint |
| I6 | Owner PIN recovery | **Another owner resets it**; break-glass `-action=reset-owner-pin` (operator only) revokes all sessions and audits `STAFF_PIN_RESET` | No email/SMS dependency |
| I7 | 02.01-B implementation plan | **Approved** as one PR: migration 000005, resolver, login/refresh/logout/me, CLI, tests | Security boundary change approved by the owner |

Setup details: [docs/Setup/WindowsDevEnvironment.md](../Setup/WindowsDevEnvironment.md).

## Email, operations & hardware (recorded 2026-10-31, third interview)

| ID | Decision | Approved option |
| ---- | ---------- | ----------------- |
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

Full evaluation and ordered backlog: [PosOperationsEvaluation2026-10.md](../Archive/PosOperationsEvaluation2026-10.md).

## Catalog, owner self-service & finance (recorded 2026-10-31, fourth interview)

| ID | Decision | Approved option |
| ---- | ---------- | ----------------- |
| K1 | Menu variants | `product_variants` with own SKU, price and recipe (Regular/Large, Hot/Iced); modifiers stay for add-ons |
| K2 | Inventory depth | Ready-to-serve items counted in units; prepared items use recipes/BOM of ingredients (g/ml/pcs) deducted on payment, minimum stock alerts, moving-average cost for HPP, stock opname, append-only movements |
| K3 | Self-service configuration | All business settings editable by the owner in the app; infrastructure secrets (Midtrans keys, SMTP, DB, DNS) stay in the server `.env` and only their status is shown |
| K4 | Subscriptions | Hostinger auto-renew + in-app expiry dates (domain/VPS/email), automatic TLS expiry check, owner email at 30/7/1 days and on failed backup |
| K5 | Midtrans settlement | No withdraw button exists; app shows expected daily settlement (gross, MDR, net) and reconciles against an imported Midtrans settlement CSV |
| K6 | Loyalty | Digital stamp card ("buy N get 1"), customer identified by a verified email (**amended by X16: verified phone**), one stamp per paid order, reward = audited 100% item discount |
| K7 | Promo & bundling | Owner-defined automatic promos (percent/amount, product/category, happy hour, period, quota), fixed-price bundles, cashier discount within O4; one best promo per line, no stacking; vouchers later |
| K8 | Static QRIS fallback | Automatic after 2 consecutive Midtrans failures or forced by the owner; cashier confirms with a reference; auto-return to dynamic QRIS after 10 minutes |
| K9 | Media storage & backups | **Cloudflare R2 Triple-Bucket Architecture**: `mycaffe-media` (public read via CDN / dev-url, presigned PUT), `mycaffe-private-media` (private signed URL TTL 15m for static QRIS & sensitive documents), and `mycaffe-backups` (strictly private, GPG AES256 encrypted pg_dump, relative path sha256 checksums, non-destructive restore guard). **B3 (2026-11-04):** product image variants are JPEG q80 200/600/1200 instead of WebP (no CGO), no blurhash yet; see api-conventions §4. |
| C-scope | Phase 02.01-C | Approved and implemented: RBAC matrix, `store_settings`, token-bound POS routes, `/me.permissions`, removal of client-supplied store/actor and of `/ws?token=` |

Designs: [CatalogInventoryDesign.md](CatalogInventoryDesign.md), requirements:
[RequirementsOwnerStaffCustomer.md](../Product/RequirementsOwnerStaffCustomer.md).

## Cleanup, single tenant & iteration 3 (recorded 2026-10-31, fifth interview)

| ID | Decision | Approved option |
| ---- | ---------- | ----------------- |
| M1 | Docker cleanup | MyCaffe + safe garbage only (stopped containers, unused anonymous volumes, unused images, build cache); other projects' volumes and the MyCaffe demo/dev volumes kept |
| M2 | Supabase | Removed from the repo, local `.env`, local MCP config, global CLI and `~/.supabase`; the Claude account connector is disconnected by the owner |
| M3 | Single-tenant MVP | Multi-tenant schema kept for future branches; optional `DEFAULT_STORE_SLUG` so staff log in with email + PIN only; demo seed keeps two stores for isolation tests; demo owner email is the owner's Gmail again in the seed (V6, PR 8.2; it was `owner.central@mycaffe.local` between PR #54 and PR 8.2) |
| M4 | Iteration 3 priority | Catalog C1–C3 with the standard CRUD contract; media upload after the R2 bucket exists |

## Iteration 4: modifiers, cart, tabs, shifts, void/refund (recorded 2026-10-31, sixth interview)

P1–P9 (negative modifier deltas, open tab rounds, split equal/by item, void stock rules, refund time
window, one shift per drawer, drawer kick + no-sale, local cart + quote, plan approval) are detailed in
[Iteration4Design.md](../Archive/Iteration4Design.md#6-decisions-recorded).

### Iteration 4c-2: Split Bill, Tab Settlement & Shift Decoupling (recorded 2026-11-01)

| ID | Topic | Decision & Technical Specification |
| ---- | ------- | ------------------------------------ |
| **P10** | **Tab Lifecycle & Split State Machine** | 1. `POST /pos/tabs/:id/close` mengunci pesanan/ronde, mengubah status tab `open` $\to$ `pending_payment` (bukan `closed`; status tersebut tidak ada di `chk_orders_status`). Ronde baru setelah ditutup ditolak dengan `409 TAB_CLOSED`.<br>2. `POST /pos/tabs/:id/reopen` mengembalikan status `pending_payment` $\to$ `open` dan membatalkan semua split (`status = 'cancelled'`, baris audit tetap di database). Reopen hanya diizinkan jika belum ada split berstatus `paid` dan tidak ada payment berstatus `pending`; jika sudah ada $\to$ `409 SPLIT_ALREADY_PAID`.<br>3. Tab tanpa permintaan split spesifik: `/close` otomatis menghasilkan 1 split bertipe `full` senilai total pesanan.<br>4. Tab bertransisi ke `status = 'completed'` dan `payment_status = 'paid'` jika dan hanya jika seluruh split non-cancelled telah lunas (`status = 'paid'`). |
| **P11** | **Split Strategies & Remainder Precision** | 1. **Scope 4c-2 dibatasi hanya untuk Equal Split ($N$) dan Split by Item**. Strategi *Custom Amount* dipindahkan ke backlog kandidat masa depan.<br>2. **Equal $N$**: Nilai $N$ antara 2 hingga 20. Sisa pembagian bulat integer Rupiah dialokasikan secara deterministik ke **Split #1**. Setiap split wajib bernilai total $> 0$.<br>3. **Split by Item**: Alokasi kuantitas utuh (1 unit tidak boleh dipecah). Seluruh kuantitas setiap item pada pesanan wajib teralokasi tepat sekali ($\sum \text{qty per item} = \text{item.quantity}$). Alokasi kurang atau berlebih ditolak `422 UNPROCESSABLE_ENTITY`. Service charge dan pajak dialokasikan proporsional terhadap subtotal split, dengan sisa pembulatan Rupiah dialokasikan ke **Split Terakhir** (*last split*). Jumlah total seluruh split wajib sama persis dengan `orders.total_amount`. |
| **P12** | **Payment Route Reuse & Concurrency Protection** | 1. Dilarang membuat endpoint pembayaran baru `/splits/:id/pay`. Menggunakan kembali rute pembayaran teruji `POST /pos/orders/:id/payments` yang diperluas dengan field opsional `split_id` (wajib untuk `billing_mode = 'open_tab'`, ditolak untuk `pay_first`).<br>2. Tender tunai menggunakan tipe pergerakan kas `sale` yang sudah ada (tidak ada tipe `SHIFT_TENDER`).<br>3. Baris split dikunci atomik (`SELECT ... FOR UPDATE`) saat inisiasi pembayaran. Maksimal satu transaksi pembayaran berstatus `pending` per split; jika ada transaksi QRIS pending lalu kasir menekan tunai/EDC pada split yang sama $\to$ ditolak `409 SPLIT_PAYMENT_PENDING` hingga QRIS expired/dibatalkan.<br>4. Webhook Midtrans melunasi tepat satu split; `gross_amount` wajib identik dengan total split bersangkutan. `commitPaidOrderTx` melunasi split terkait; pesanan utama baru ditandai lunas saat seluruh split non-cancelled selesai dibayar. |
| **P13** | **Shift Attribution on Payments** | Menambahkan kolom `payments.shift_id` (nullable, FK ke `shifts.id` ON DELETE RESTRICT) via migrasi additive `000012`. Kolom diisi dengan ID shift kasir yang aktif saat transaksi pembayaran dieksekusi (termasuk non-tunai jika kasir sedang membuka shift). Agregasi laporan X-Report & Z-Report menghitung pelunasan tab berdasarkan shift saat pembayaran dibayar (`payments.shift_id`), bukan shift saat tab dibuka (`orders.shift_id`). |
| **P14** | **Shift Decoupling & Unsettled Open Tabs Warning** | Penutupan shift (`POST /pos/shifts/current/close`) tidak diblokir oleh adanya tab yang belum lunas. Laporan penutupan shift (X-Report & Z-Report) memuat ringkasan peringatan: `unsettled_open_tabs_count`, total nilai rupiah tab gantung, dan daftar `order_number` terkait (baik status `open` maupun `pending_payment`). Tab yang melampaui hari bisnis ditandai khusus. |

## Hotfix v0.4.3: void/refund money and stock rules (recorded 2026-11-01, owner audit of v0.4.2)

| ID | Rule (implemented) |
|----|--------------------|
| HF1 | Void only while no money is attached: payment_status `unpaid`/`failed`/`expired` and no payment row `pending`, `authorized`, `paid`, `partially_refunded` or `refunded` on the order or its splits; otherwise 409 `ORDER_HAS_PAYMENTS` (use refund) |
| HF2 | Stock is returned by one shared helper (products **and** modifier options) only while `stock_status` is `reserved`/`committed`, then set to `released`; void, expiry, reconciliation and refund all use it, so stock never comes back twice. Void also cancels every `order_rounds` row and writes `order.cancelled` to the outbox in the same transaction |
| HF3 | Refund restock: `restore_stock` is required (no default). A `by_item` split restores only its own `payment_split_items`; `full`/`equal` restore the whole order once, when the refund makes the order fully refunded (decided by that last refund's flag) |
| HF4 | Refunds start only when the order is `paid` or `partially_refunded` (a half-paid tab is still being collected). `payments.refunded_amount`, `orders.refunded_amount` and the split status (`refunded`) are updated; `payment_splits.paid_amount` stays gross because settlement recomputes the order's paid amount from it |
| HF5 | PINs re-entered for void, refund, stock override and shift variance use the actor's login counter (`STAFF_PIN_MAX_FAILURES` wrong → locked `STAFF_PIN_LOCKOUT`, which also ends running sessions). **Updated 2026-11-04 (review follow-up F1):** wrong owner PINs typed by other staff no longer advance or lock any owner account; they are audited `OWNER_PIN_FAILED` and capped per actor (`STAFF_PIN_MAX_FAILURES` per window → 429 `PIN_ATTEMPTS_EXCEEDED`) and per store (3× that from all staff → 429 `OWNER_PIN_STORE_LIMIT` for every owner-PIN approval, and each active owner gets one `owner_pin_attempts` email). An owner typing a wrong PIN still advances their own login counter. Replaces the earlier accepted risk that a cashier could lock the owner out |
| HF6 | Stock override no longer turns tracking on silently: untracked product → 422 `PRODUCT_NOT_TRACKED` unless `enable_tracking: true`; unknown product → 404 |

## Catalog cleanup (recorded 2026-11-01)

| ID | Decision |
|----|----------|
| ADR4 | Catalog cleanup done now, before Iteration 5: pricing reads only the variant price, `products.base_price` kept as a deprecated mirror, unit stock stays on products (variants share it), DB CHECK single-choice groups `max_select = 1`. See [ADR-004](Adr004CatalogSchemaModernization.md) |

## Iteration 5 (recorded 2026-11-01, seventh interview)

Plan: [Iteration5TableQrKdsPlan.md](../Archive/Iteration5TableQrKdsPlan.md).

| ID | Decision | Approved option |
|----|----------|-----------------|
| QR1 | Printed table QR | Opaque 256-bit random token, rotatable by the owner (old sticker stops at once), unique lookup across stores. **Supersedes Q5.** Pause via `qr_ordering_paused` (store) and `tables.is_active` (table) |
| QR2 | New SECURITY DEFINER functions | `resolve_table_qr` (5a), `track_order_by_token` (5b), `list_stores_with_pending_outbox` (5c); minimal output, owned by `mycaffe_migrator`, EXECUTE for `mycaffe_app` only; `AGENTS.md` allow-list updated in the PR that adds each |
| QR3 | Table management | New permission `table.manage`, **owner only**; closing a table session stays with cashier/owner |
| QR4 | Store closed for QR ordering | No shift open on any drawer → 409 `STORE_CLOSED` (no opening-hours setting) |
| QR5 | Public anti-abuse | 2 unpaid orders per guest session, 6 per table session; 10 order attempts per guest session / 10 min; 10 new sessions per IP / 10 min; Nginx 30 req/min per IP (burst 20); ≤ 30 items per order, quantity ≤ 20 per line; constants, not store settings |
| QR6 | Public order tracker | Link valid until 24 h after the order completes; the R1 digital receipt keeps its own 90-day token |
| QR7 | Table session auto-close | Lazy: status derived at read time, session closed on the next read/scan after `settled_at + table_idle_close_minutes`; no worker |

## Offline cash & static QRIS (recorded 2026-11-02, eighth interview)

ADR: [Adr005OfflineCashSync.md](Adr005OfflineCashSync.md). Status: decided, implementation planned.

| ID | Decision | Approved option |
|----|----------|-----------------|
| OF1 | Offline scope | Fully paid `takeaway`/`dine_in` orders by cash or static QRIS only; no tabs, splits, void, refund, gateway payments |
| OF2 | Offline identity | Owner-registered POS device + local PIN against a per-device verifier (72 h expiry, wiped on revoke, 5-fail local lockout, failures uploaded and audited) |
| OF3 | Offline pricing | Server re-prices from its stored snapshot of the device's `catalog_version`; older than 24 h → accepted and flagged `stale_catalog`; delta reported |
| OF4 | Offline shift | Order stays in the shift open when it was taken; shift close refused while the device reports unsynced orders (owner force-close allowed); late orders mark the Z-report `amended` |
| OF5 | Offline stock | Never rejected for stock; balance may go negative, line flagged `oversold` (stock CHECK change needs its own approval) |
| OF6 | Offline numbering | Device-prefixed local queue number on the receipt; official number assigned by the server at sync |
| OF7 | Offline limits | 12 h or 300 orders since last sync, per-store settings |
| OF8 | Offline static QRIS | Amount + reference required; stored `offline_unreconciled` until settlement matching |
| OF9 | Failed sync | Never discarded: stored as `rejected` with a reason, owner accepts or rejects, both audited |

## Non-cash refunds (recorded 2026-11-02, ninth interview)

Status: **RF1-c and RF1-e implemented** (B4, migration 000023, 2026-11-04): full refunds of any method in cash from the drawer or by the owner's manual bank transfer, ledger `payment_refunds`, per-role windows in `store_settings`; a cashier past the cashier window needs the owner PIN. Partial refunds (RF1-b), Midtrans Direct Refund (RF1-a), EDC reversal (RF1-d) and `refund_pending` (RF1-f, moves with RF1-a) are still planned.
Midtrans facts checked against docs.midtrans.com on 2026-11-02: QRIS is charged with acquirer `gopay` (`internal/pkg/midtrans/midtrans.go`); GoPay QRIS refund window is 45 days on-us and 7 days off-us; Direct Refund returns 200 (approved) or 202 (denied by bank); the final signal is the refund notification carrying `bank_confirmed_at`; `refund_key` retries are honoured for 7 days. Partial refund support for GoPay QRIS is not stated in the docs.

| ID | Decision | Approved option |
|----|----------|-----------------|
| RF1-a | Dynamic QRIS | Midtrans Direct Refund first, within the provider window, with a deterministic `refund_key` per refund; denied (202) or out of window → fallback RF1-c |
| RF1-b | Partial QRIS refund (partial refunds by order line exist since PR 8.5a for every manual source) | API for full refunds only; partial refunds use RF1-c until partial support is proven in sandbox (then enabled per acquirer by configuration) |
| RF1-c | Fallback (also static QRIS, EDC, partial) | Either cash from the open drawer as a `refund` cash movement keeping the original method and reference, or a manual bank transfer by the **owner only**, recorded with bank name, last 4 account digits and transfer reference (no full account number stored); the transfer does not touch the drawer |
| RF1-d | EDC | Void/reversal on the EDC terminal allowed when the approval code / trace number is recorded; status stays pending reconciliation until matched to the bank settlement report |
| RF1-e | Refund time window (closes gap P5) | Cashier: same business day only. Owner: up to 7 days. Older: no refund in the system. Values in `store_settings` |
| RF1-f | Provider confirmation | API 200 → `refund_pending`; `refunded` only after the `bank_confirmed_at` notification or a confirming status check. Existing `void_refund_owner_threshold` applies to every method |

## Infrastructure & operations (recorded 2026-11-02, tenth interview)

Status: decided, implementation planned ([audit tickets](../Engineering/AuditBacklog.md) P0-03, P0-04, P0-07, P2-04; P0-03 and P0-04 done).

| ID | Decision | Approved option |
|----|----------|-----------------|
| IN1 | Production ingress | **Named Cloudflare Tunnel** per client VPS, same pattern as the demo (I2): no inbound ports except SSH from admin IPs, TLS at the Cloudflare edge, client IP from `CF-Connecting-IP` (Nginx `real_ip_header` + `set_real_ip_from` the cloudflared container network; `TRUSTED_PROXIES` set), certbot removed. Changing `docker-compose.prod.yml` and `nginx.prod.conf` still needs approval as a deployment change. If the tunnel is down, cash and static QRIS continue through ADR-005 offline mode |
| IN2 | Branch protection | **Stay on the free plan; manual discipline.** No technical branch protection (private repo, API 403). Rules in [GitWorkflowAndReleases.md](../Engineering/GitWorkflowAndReleases.md) §3 apply; the risk of a merge without approval (PR #23) is accepted by the owner |
| IN3 | Monitoring & alerting | **Grafana Cloud free tier**: external uptime check on `/health/ready` plus metrics via remote write; technical alerts to the operator's Telegram group and email. Store owners get business notifications from MyCaffe only, not technical alerts |
| IN4 | Accounting export | **Neutral CSV first**: generic journal columns (date, account, debit, credit, reference) with configurable account mapping; adapters for Jurnal/Accurate only when a client uses one |

## Demo v0.7 (recorded 2026-11-04, eleventh interview)

Status: decided. Scope and checklist: [DemoReadinessChecklist.md](../Setup/DemoReadinessChecklist.md);
gaps: [FrontendIntegrationContract.md](FrontendIntegrationContract.md) §5.

| ID | Decision | Approved option |
|----|----------|-----------------|
| DM1 | First demo audience | **Prospective cafe client**: shows the full operational flow, not investor dashboards |
| DM2 | Demo format | **On site at the client's cafe**, with a recorded video as fallback |
| DM3 | Hardware | **Minimal kit**: Android tablet (POS), second tablet or laptop (KDS), client's phone (QR guest), Bluetooth 58 mm thermal printer. Not brought: cash drawer (G-6), EDC, queue TV (G-3) |
| DM4 | Demo server | **Laptop + Cloudflare Quick Tunnel** (no cost). The tunnel is not restarted after the table QR codes are printed; the devices use the owner's phone hotspot. Domain and VPS purchase are deferred; IN1/P0-07 stays planned |
| DM5 | Demo flows | Core flows **plus dine-in tab and split bill**: QR order + QRIS sandbox, KDS, cashier shift + cash + receipt, void with owner approval, Z report and sales report, tab + split. Not shown: refund, media upload |
| DM6 | Printing | **PWA + RawBT**: the frontend sends `GET /pos/orders/:id/receipt?format=escpos` bytes to RawBT; no backend change. A native wrapper (Capacitor) waits for a paying client |
| DM7 | Remote owner approval (P1-04, G-5) | **Built before the demo.** Single-use, bound to action + order + amount, expires in 2 minutes, audited; the tablet `owner_pin` stays as fallback. Design and API contract need owner approval before coding (auth/permission gate) |
| DM8 | Approval confirmation on the owner's phone | **Owner PIN entered on the phone**, verified by the existing PIN check and shop-wide guess limit (HF5). WebAuthn biometrics deferred until a stable domain exists |

Backend v0.7 scope from DM5/DM7: G-1 order list/detail, G-2 open tab list, G-5 remote approval, and
demo leak fixes L-1..L-3.

## Release train v0.8–v0.12 (recorded 2026-11-04, twelfth interview)

Status: decided. Lifts the 2026-11-03 feature freeze. Benchmark: the majoo "kasir online" feature list
(competitor review in [CompetitorAnalysisMajoo.md](../Product/CompetitorAnalysisMajoo.md)).

| ID | Decision | Approved option |
|----|----------|-----------------|
| V1 | Feature freeze | **Lifted entirely**: offline, inventory, promotions/loyalty, delivery channels and services are in scope |
| V2 | Release order | **v0.8** money & operations → **v0.9** cafe floor → **v0.10** per-device offline → **v0.11** channels & customers → **v0.12** services workflow. Go-live is possible after v0.8/v0.9 |
| V3 | Approval scope | The owner pre-approves all design gates (new dependencies incl. CGO, additive migrations, new API contracts, auth/permission changes recorded in an ADR) **and merges into `staging` once CI is green**. Still asked: `staging` → `main` promotion, `v*` tags, destructive migrations, live `.env` edits, production deploys, real-money calls. Replaces the per-PR approval rule after PR #23 |
| V4 | SMS OTP use | **Staff PIN reset and customer phone verification** (before WhatsApp/SMS receipts, points, deposit or invoices); not on every staff login. Driver `log` in development/test, a real provider in demo/production |
| V5 | Messaging providers | **Official channels, two providers**: WhatsApp Business Cloud API (Meta) and a local Indonesian SMS aggregator, behind one sender interface like `mailer`. No unofficial WhatsApp Web gateways |
| V6 | Demo e-mail | Seeded demo owner e-mail is again the owner's Gmail **in the repository seed** (reverses the e-mail part of leak fix L-3; amends M3). The SMTP sender account and its app password stay in the local `.env.demo` |
| V7 | Redis | **Deferred** until the API runs as more than one instance or PostgreSQL queue latency is measured as a problem; PostgreSQL keeps OTPs, queues and rate limits. Trigger and design recorded in [Adr008DataPlatformAndScaling.md](Adr008DataPlatformAndScaling.md) |
| V8 | Domain | **Still none.** P0-07 is prepared as configuration only; Web Push and real WhatsApp sending wait for a domain (Meta business verification, stable origin) |
| V9 | Offline model (v0.10) | **Per device** as accepted in ADR-005, plus a LAN kitchen printer fallback; a LAN master "local server" is v0.10b only if a real cafe needs it. I2 stays |
| V10 | "Owner scope out" | Only **HR/teams** features (staff schedules, attendance, leave, staff-facing reservations) are out; owner endpoints that cashier features need stay in. Frontend is a separate repository |
| V11 | Bill rounding | **Cash only, to the nearest** store-configured step (100 or 500), off by default; stored as `rounding_amount`, printed as its own receipt line, not taxed, reported per day and shift |
| V12 | Multi-payment (implemented in PR 8.3: a pay-first tender of the **whole** remaining balance still supersedes a pending QRIS so a customer can switch to cash; duplicate settlement detection covers a late QR payment. Partial tenders and open tabs stay locked while a QRIS is pending) | Direct methods (cash, EDC, static QRIS, transfer) combine freely; **dynamic QRIS at most once, for exactly the remaining balance, as the last payment**, and it locks other tenders while pending. Change only from cash. Compliment = owner-approved 100 % discount, not a method. Deposit/points/coupons in v0.11 |
| V13 | Outlet cash | **Separate fund** from the cashier drawer: append-only `outlet_cash_movements` (owner top-up, categorised expense with optional receipt photo, refund, transfer to/from a drawer), daily expense limit with ADR-007 approval (`outlet_expense`), third refund source for RF1-c. Cashier settings: default opening float, daily limit, blind or visible count at close |
| V14 | Staff ID card | **QR + PIN**: the badge QR is a revocable random identity token that replaces typing the e-mail; the 6-digit PIN and its limits stay mandatory |
| V15 | Demo timing | The prospective-client demo **waits for v0.8** |

## Environments and v0.9 cafe floor (recorded 2026-11-05, thirteenth interview)

Status: decided. The owner bought `abbdullaziz.online` (supersedes V8 "no domain") and widened the agent's
permissions to shell, `curl` and `.env*` files.

| ID | Decision | Approved option |
|----|----------|-----------------|
| W1 | PR #65 (Cloudflare, Sentry, SMS bridges, `routes.go`) | **Rebased onto `staging` and cleaned** by the agent: v0.8 routes moved into `routes.go`, demo PINs removed from `.env.demo.example` and rotated in the local `.env.demo` |
| W2 | Where the public demo runs | **WSL Ubuntu 24.04 as a VPS stand-in**: Docker Engine, ufw, GHCR images by digest through `deploy.sh`, cron, Named Tunnel `demo.abbdullaziz.online`. Docker Desktop stays for development and integration tests |
| W3 | Frontend absent | **API-only demo**; the `web` service becomes an optional compose profile, enabled by `deploy.sh` when a web digest is given; Nginx answers a status page while it is absent |
| W4 | OTP delivery in the demo | **`SMS_DRIVER=email`** to the owner's Gmail; production stays `disabled` until an SMS aggregator is chosen. The owner pastes a new Google App Password into `.env.demo` himself |
| W5 | Merge tables | **One shared session**: the merged table's session links to the main session, its orders move there, both QR codes order into it; split unlinks; move transfers the whole session to an empty table. Audited and published on the POS channel |
| W6 | Table timer | **Warnings only**: `table.timer_warning` (default 10 minutes before) and `table.timer_expired`; `ends_at` in the database scanned every 30 s; extensions audited; orders stay open after expiry |
| W7 | Queue TV | **Per-store display token** (`store.id.secret`, hash only, revocable): read-only public endpoint and WebSocket channel `queue` with queue numbers and status only |
| W8 | Kitchen, checker and label printing | **Server print-job queue**: `printers` (station, `escpos` or `tspl`, width, copies, footer, agent token) and `print_jobs` created on payment and round submit; a print agent claims, prints and acknowledges; retries and dead-letter like the outbox |
| W9 | Digital receipts | **Email live; WhatsApp Cloud API driver built** and switched on when the owner adds the Meta token and phone number id; SMS sends the link only to verified phones (V4). Every send is audited and rate limited |
| W10 | Order of work | **Environment first**: E1 PR #65, E2 WSL demo with `v0.8.0` by digest, E3 real-environment tests with `curl`; then v0.9 docs (PRD, brand, analytics) and PRs 9.1–9.4 |

## CI runner, demo WhatsApp, print agent and v0.10 offline (recorded 2026-11-06, fourteenth interview)

Status: decided. Context: GitHub-hosted runners stopped on a billing failure right after v0.9.0 was tagged.

| ID | Decision | Approved option |
|----|----------|-----------------|
| W11 | CI and releases without hosted-runner billing | **Self-hosted runner on the WSL demo host** (`wsl-mycaffe`, labels `self-hosted, linux, mycaffe`, systemd service, user `runner` in the docker group). The repository stays private; making it public even briefly was rejected (history and code would be exposed for good) **Amended 2026-11-06:** the owner made the repository public; CI and releases moved back to `ubuntu-latest` (free for public repositories) and the WSL runner is stopped, because a fork pull request could otherwise run its own workflow on the demo host. Restart it only if the repository goes private again |
| W12 | WhatsApp for demo and development | **`cmd/wa-bridge` with whatsmeow** (its own Go module) as a separate container in the demo/dev compose only; the API uses a `bridge` WhatsApp driver that the config refuses in production. Session in a SQLite file on the bridge volume (amended 2026-11-06 from "own PostgreSQL schema": keeps the bridge out of the application database and its migration chain), pairing by QR from the bridge log, a dedicated demo number (never stored in the repository). Production keeps the Cloud API driver |
| W13 | First print agent platform | **Go binary `cmd/print-agent`** (Windows service, Linux/ARM systemd) on a LAN device, printing to `IP:9100`. An Android app follows only when a client has no other device |
| W14 | WhatsApp receipt template | `struk_digital`, category **Utility**, language `id`, three parameters (store, order number, link): "Terima kasih sudah berbelanja di {{1}}. Struk digital pesanan {{2}} bisa dilihat di {{3}}. Tautan berlaku 90 hari." The bridge renders the same text |
| W15 | Table timer per area | **Deferred**: one limit per store. `area_settings (section, time_limit_minutes)` overrides it only when a client asks |
| W16 | v0.8 features offline (amends ADR-005 OF1) | Cash rounding applies (server recomputes); discounts only up to `cashier_max_discount_bps`, no owner-approved discount or compliment; no multi-payment; badge + PIN allowed with the device-held verifier |
| W17 | Negative stock (ADR-005 §5 item 1) | **Approved**: tracked stock may go below zero only through offline sync; lines flagged `oversold` and audited; online reservation still refuses. Every reader of `stock_quantity` is checked before the migration |
| W18 | Kitchen printing during an outage | The print agent serves `POST :8787/print` on the LAN with a per-agent local key; it renders with the shared `internal/pkg/printfmt` and marks tickets OFFLINE, using its cached printer/station config. Orders synced with `printed_locally` get no server print job |
| W19 | Offline PIN verifier (ADR-005 OF2) | Argon2id(PIN, per-device salt), 64 MB / 3 iterations, 72 h expiry, device lock after 5 failures, revocation from the owner's phone as the main control. Residual risk (GPU brute force of a copied tablet within 72 h) accepted |
| W20 | Catalog snapshots (ADR-005 OF3) | Stored **when the catalog is served** to a device: version = SHA-256 of the pricing content, insert-once; `stale_catalog` measured from first serve |
| W21 | Order of work | (1) runner + v0.9.0 build, (2) wa-bridge, (3) printfmt + print-agent → v0.9.1, (4) ADR-005 update, (5–9) PRs 10.1 devices/verifiers/snapshots, 10.2 offline sync + owner review, 10.3 negative stock, 10.4 agent LAN printing, 10.5 pending-sync shifts and amended Z-reports → v0.10.0 |

## v0.11 promotions and inventory (recorded 2026-11-06, fifteenth interview)

| ID | Decision | Approved option |
|----|----------|-----------------|
| X1 | v0.11 scope | **Two releases.** v0.11.0 = promotions & bundles (K7), then ingredient inventory with recipes and COGS (K2). v0.11.1 = per-channel (ojol) prices, stamp card (K6), invoices. Deposit, points and coupons (V12) last: customer money, needs verified phones and refunds |
| X2 | Promo and cashier discount | Line promos apply first; the cashier order discount (O4) may still be added and is computed on, and limited by `cashier_max_discount_bps` of, the subtotal **after** promos. Receipts show both |
| X3 | Promos offline | Active promo rules are part of the catalog snapshot (W20, version changes with them). Sync re-evaluates promos from the snapshot at `offline_created_at` (happy hour follows the order time). A quota already used up at sync still keeps the promo price and flags `promo_over_quota` for the owner |
| X4 | Bundles | A bundle is **its own catalog item** with components (a component may offer choices); component stock and recipes are deducted and each component prints at its own station/KDS. For per-menu sales and COGS the bundle price is allocated to components in proportion to their normal prices. Automatic mix & match in the cart comes later |
| X5 | Short ingredient stock | **Never blocks a sale**: recipe quantities are estimates, ingredient balances may go negative, low/negative alerts go to the owner and the stock count corrects them. Unit stock of ready-to-serve products and the manual sold-out switch keep blocking online sales |
| X6 | Recipes | Defined **per variant and per modifier option** (incl. sub-addons); an option may add or substitute ingredients (e.g. oat milk: -150 ml milk, +150 ml oat). An option without a recipe uses none |
| X7 | Purchasing | **Full module**: suppliers (name, contact, payment terms in days), purchase orders draft → sent → partly/fully received → closed, staged goods receipts that add stock and update the moving-average cost at the PO price (correctable on receipt), accounts payable per receipt with due date from the terms, paid from outlet cash (V13 limits and ADR-007 apply), transfer or cash, in instalments. A direct purchase without a PO stays as the quick path. No supplier returns, input tax or multiple warehouses |
| X8 | Inventory permissions | Two new permissions: `inventory.count` (cashier, barista, kitchen: stock counts and waste) and `inventory.manage` (owner: ingredients, recipes, suppliers, POs, purchases, payables). A count variance or waste above a store rupiah limit per session (default Rp50.000) posts only after owner approval (own PIN or ADR-007); below it posts at once, audited |
| X9 | Ready-to-serve stock | Stays in `products.stock_quantity` (online blocking, offline negative stock and stock override unchanged). Purchasing, counts, waste and moving-average cost serve both kinds of stock item: ingredients and ready-to-serve products |
| X10 | COGS capture | Default recorded without a question: the cost of goods sold is stored **per order line when the order is paid**, so margin reports do not move when ingredient prices change later |
| X11 | Order of work | 11.1 promo engine (rules, happy hour, quota, per line, quote and guest QR, offline snapshot) → 11.2 bundles → 11.3 ingredients, recipes, deduction on payment, alerts, COGS per line → 11.4 suppliers, POs, staged receipts, direct purchases, moving average → 11.5 payables and payments → 11.6 stock counts and waste with approval, COGS and gross-profit report per menu → v0.11.0 |

## v0.11.1 channels, loyalty and invoices (recorded 2026-11-07, sixteenth interview)

| ID | Decision | Approved option |
|----|----------|-----------------|
| X12 | Delivery-app orders | **Entered by hand at the POS**: the cashier copies the GoFood/GrabFood/ShopeeFood order and picks its sales channel, which sets the prices. No platform API integration |
| X13 | Channel prices | An **absolute price per variant per channel**; a variant without a channel price keeps its normal price; modifier prices stay the same on every channel |
| X14 | Channel payment | A new payment method `platform`: the order is paid at once, never touches the cash drawer or the cash figures of the Z-report. Each channel has a commission % (estimate) so reports show gross, commission and net per channel. Platform payouts are not reconciled |
| X15 | Rules on channel orders | Automatic promotions, the cashier discount, store tax/service charge and stamps **do not apply** to channel orders: the channel price is final |
| X16 | Loyalty identity (amends K6) | Stamp-card customers are identified by their **verified phone number** (V4, existing `customers` and OTP), not by email |
| X17 | Stamp card rules | One card per store; **one stamp per paid order at or above a minimum** amount; after N stamps the customer gets one free item from an owner-chosen list (an audited 100% line discount); each stamp expires after a set period; a full refund removes the order's stamp. N, minimum and expiry are owner settings (defaults 10, Rp25.000, 6 months). Not on channel orders (X15) |
| X18 | Invoices | **B2B pay later**: business customers (name, optional tax ID, e-mail, phone, payment terms in days); orders charged to the account become an invoice with a due date; the receivable is paid in instalments by transfer, cash or QRIS; PDF download; automatic reminders by e-mail/WhatsApp at D-3, the due date and D+7. No e-Faktur |
| X19 | Order of work | 11.7 sales channels, channel prices, `platform` method, report per channel → 11.8 orders linked to customers, stamp card, reward redemption → 11.9 business customers, invoices, receivables and instalments, PDF → 11.10 invoice reminders → v0.11.1 |

## Awaiting owner approval

None. The G-1/G-2 contract (PR #55) and ADR-007 (G-5) were approved on 2026-11-04.

## Resolved from existing sources (not re-asked)

| Topic | Resolution | Source |
| ------- | ----------- | -------- |
| Split bill semantics | Approved in Iteration 4 (P3, P10–P14): equal N (remainder to split 1) and by item (proportional tax/service, remainder to last split); locked after first split payment; payments use POST /pos/orders/:id/payments with split_id; payments.shift_id attribution. Scheduled for Iteration 4c-2. | `docs/Archive/Iteration4Design.md#6-decisions-recorded` (P3), `DecisionRegister.md` (P10–P14) |
| Roles | Exactly `owner`, `cashier`, `barista`, `kitchen`; no new roles. | `AGENTS.md`, migration `000001` |

## Still open

| Topic | Needed for | Default until decided |
| ------- | ----------- | ----------------------- |
| Production domain name | Tunnel hostname (IN1), cookie domain (O2) | Quick Tunnel URL for demo only |
| Midtrans production credentials & VPS provisioning | Production cutover | Sandbox only |
