# Decision Register

Owner decisions that bind implementation. Recorded 2026-10-31 (structured interview with the owner).
Earlier decisions live in [decisions-before-code.md](decisions-before-code.md) and ADR-003
(PR #7, `docs/architecture/adr-003-staff-authentication-session-and-rbac.md`).

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
| Q5 | Printed table QR lifetime | **No time expiry.** Version-based revocation (`qr_version`) per table, signing-key rotation via key ID, plus a per-store **emergency pause** that disables all QR session creation instantly. | Dedicated QR signing key (never `JWT_SECRET_KEY`). | 02.01-D |
| Q6 | Guest sessions | **One session per device**; a guest sees only orders bound to its own session. TTL **3 h**, or ends when all of its orders are paid and completed. | Distinct `aud`/`typ` from staff tokens. | 02.01-E |
| Q7 | Late settlement after expiry | Keep current behavior: revive the order to the queue if stock allows, otherwise cancel + `payment.late_settlement_stock_exhausted`. **Add a "late payment" flag** visible on KDS and cashier screens. | Flag not implemented yet. | KDS (Phase 03) |
| Q8 | Demo / staging environment | Local Docker Compose stack + **Cloudflare Quick Tunnel**, auto-restart, `APP_ENV=staging`, Midtrans sandbox | [docs/setup/demo-staging-environment.md](../setup/demo-staging-environment.md) | — |

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

Setup details: [docs/setup/windows-dev-environment.md](../setup/windows-dev-environment.md).

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

Full evaluation and ordered backlog: [pos-operations-evaluation.md](pos-operations-evaluation.md).

## Catalog, owner self-service & finance (recorded 2026-10-31, fourth interview)

| ID | Decision | Approved option |
| ---- | ---------- | ----------------- |
| K1 | Menu variants | `product_variants` with own SKU, price and recipe (Regular/Large, Hot/Iced); modifiers stay for add-ons |
| K2 | Inventory depth | Ready-to-serve items counted in units; prepared items use recipes/BOM of ingredients (g/ml/pcs) deducted on payment, minimum stock alerts, moving-average cost for HPP, stock opname, append-only movements |
| K3 | Self-service configuration | All business settings editable by the owner in the app; infrastructure secrets (Midtrans keys, SMTP, DB, DNS) stay in the server `.env` and only their status is shown |
| K4 | Subscriptions | Hostinger auto-renew + in-app expiry dates (domain/VPS/email), automatic TLS expiry check, owner email at 30/7/1 days and on failed backup |
| K5 | Midtrans settlement | No withdraw button exists; app shows expected daily settlement (gross, MDR, net) and reconciles against an imported Midtrans settlement CSV |
| K6 | Loyalty | Digital stamp card ("buy N get 1"), customer identified by a verified email, one stamp per paid order, reward = audited 100% item discount |
| K7 | Promo & bundling | Owner-defined automatic promos (percent/amount, product/category, happy hour, period, quota), fixed-price bundles, cashier discount within O4; one best promo per line, no stacking; vouchers later |
| K8 | Static QRIS fallback | Automatic after 2 consecutive Midtrans failures or forced by the owner; cashier confirms with a reference; auto-return to dynamic QRIS after 10 minutes |
| K9 | Media storage & backups | **Cloudflare R2 Triple-Bucket Architecture**: `mycaffe-media` (public read via CDN / dev-url, presigned PUT), `mycaffe-private-media` (private signed URL TTL 15m for static QRIS & sensitive documents), and `mycaffe-backups` (strictly private, GPG AES256 encrypted pg_dump, relative path sha256 checksums, non-destructive restore guard). |
| C-scope | Phase 02.01-C | Approved and implemented: RBAC matrix, `store_settings`, token-bound POS routes, `/me.permissions`, removal of client-supplied store/actor and of `/ws?token=` |

Designs: [catalog-inventory-design.md](catalog-inventory-design.md), requirements:
[../product/requirements-owner-staff-customer.md](../product/requirements-owner-staff-customer.md).

## Cleanup, single tenant & iteration 3 (recorded 2026-10-31, fifth interview)

| ID | Decision | Approved option |
| ---- | ---------- | ----------------- |
| M1 | Docker cleanup | MyCaffe + safe garbage only (stopped containers, unused anonymous volumes, unused images, build cache); other projects' volumes and the MyCaffe demo/dev volumes kept |
| M2 | Supabase | Removed from the repo, local `.env`, local MCP config, global CLI and `~/.supabase`; the Claude account connector is disconnected by the owner |
| M3 | Single-tenant MVP | Multi-tenant schema kept for future branches; optional `DEFAULT_STORE_SLUG` so staff log in with email + PIN only; demo seed keeps two stores for isolation tests; demo owner email `abdulaziz543333@gmail.com` |
| M4 | Iteration 3 priority | Catalog C1–C3 with the standard CRUD contract; media upload after the R2 bucket exists |

## Iteration 4: modifiers, cart, tabs, shifts, void/refund (recorded 2026-10-31, sixth interview)

P1–P9 (negative modifier deltas, open tab rounds, split equal/by item, void stock rules, refund time
window, one shift per drawer, drawer kick + no-sale, local cart + quote, plan approval) are detailed in
[iteration-4-design.md](iteration-4-design.md#6-decisions-recorded).

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
| HF5 | PINs re-entered for void, refund, stock override and shift variance use the login counter (5 wrong → locked 15 min, which also ends running sessions). Wrong owner PINs advance every unlocked owner's counter (the targeted owner is unknown) and are audited `OWNER_PIN_FAILED`; 5 of them by one actor in 15 min → 429 `PIN_ATTEMPTS_EXCEEDED`. Accepted risk: a cashier can lock the owner out for 15 min with 5 guesses (same as guessing the owner's login) |
| HF6 | Stock override no longer turns tracking on silently: untracked product → 422 `PRODUCT_NOT_TRACKED` unless `enable_tracking: true`; unknown product → 404 |

## Awaiting owner approval

| ID | Proposal | Until approved |
|----|----------|----------------|
| RF1 | **Non-cash refunds.** (a) Dynamic QRIS paid through Midtrans: call the Midtrans refund API when it supports that transaction, record the provider refund id, mark refunded only on the provider's success (webhook/status check). (b) Fallback for any non-cash payment, and for (a) when the API refuses: cash from the open drawer as a `refund` movement whose reason and audit metadata keep the original method and reference. (c) EDC: cash refund as in (b), or manual reversal on the EDC terminal recorded with its reference number and reconciled later. Static QRIS: (b) only (no provider API). | Non-cash refund → 409 `REFUND_METHOD_NOT_SUPPORTED`; nothing is marked refunded |

## Resolved from existing sources (not re-asked)

| Topic | Resolution | Source |
| ------- | ----------- | -------- |
| Split bill semantics | Approved in Iteration 4 (P3, P10–P14): equal N (remainder to split 1) and by item (proportional tax/service, remainder to last split); locked after first split payment; payments use POST /pos/orders/:id/payments with split_id; payments.shift_id attribution. Scheduled for Iteration 4c-2. | `docs/architecture/iteration-4-design.md#6-decisions-recorded` (P3), `decision-register.md` (P10–P14) |
| Roles | Exactly `owner`, `cashier`, `barista`, `kitchen`; no new roles. | `AGENTS.md`, migration `000001` |

## Still open

| Topic | Needed for | Default until decided |
| ------- | ----------- | ----------------------- |
| Production domain name & DNS | Named Cloudflare Tunnel / TLS, cookie domain (O2) | Quick Tunnel URL for demo only |
| Midtrans production credentials & VPS provisioning | Production cutover | Sandbox only |
| WebSocket ticket lifecycle (TTL, single use) | Phase 03 realtime | `/ws` stays `503` in staging/production |
