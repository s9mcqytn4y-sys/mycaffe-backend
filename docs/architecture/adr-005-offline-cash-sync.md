# ADR-005: Offline Cash & Static QRIS Ordering with Server Sync

- **Status**: Accepted (owner, 2026-11-02): decisions OF1–OF9 below. Implementation **Planned**; nothing is built yet.
- **Deciders**: Product owner, backend
- **Amends**: ADR-003 (adds registered POS devices as a second credential type); keeps the AGENTS.md rule that offline UI caching does not make online payments or cross-device sync work.
- **Related**: decision register K8 (static QRIS fallback), QR5 (anti-abuse), HF2 (shared stock helper); audit report Tahap 1, ticket P0-10.

## 1. Context

The first pilot is one external cafe on a dedicated VPS (Model A). The owner requires the cashier to keep
taking money when the cafe's internet is down. Today every POS action needs the server: pricing, order
numbers, stock and payments are server-side only, and a cash tender fails closed without an open shift
(`internal/service/payment_initiate.go`, `ErrNoOpenShift`). Without an offline path, staff fall back to
paper slips that are often never typed in, which hides revenue and opens room for fraud.

The goal is a narrow offline mode: the device records what happened, the server stays the only source of
truth for order numbers, totals, stock, shift attribution and reports, and nothing offline is shown to the
owner as final until the server has accepted it.

## 2. Decisions

| ID | Decision | Rationale |
|----|----------|-----------|
| OF1 | **Scope.** Offline orders are `takeaway` or `dine_in` without a tab or table session, paid in full at once by **cash** or **static QRIS**. Not offline: tabs and rounds, split bill, void, refund, dynamic QRIS/gateway, guest QR ordering, KDS across devices, reports, staff admin. | Fully paid single orders cannot conflict with another device's edits; everything else depends on shared state. |
| OF2 | **Identity: registered device + local PIN.** The owner registers each POS device (device credential separate from staff sessions). Staff can switch cashier offline with a PIN checked on the device against a **per-device PIN verifier** issued by the server (never the server's PIN hash). Verifiers expire after **72 h**, are wiped when the device is revoked, and the device locks a staff member after **5 wrong PINs**; failed attempts are uploaded with the next sync and written to `audit_logs`. Sync calls are authenticated by the device credential. | Owner choice. A stolen tablet allows offline guessing of a 6-digit PIN, so the verifier is device-bound and short-lived and device revocation is the main control. |
| OF3 | **Pricing.** The device sends `catalog_version` with each order; the server re-prices from its **stored snapshot of that version**, never from client amounts. Versions older than 24 h are accepted but flagged `stale_catalog`. The difference to the current price is stored and reported. | The customer paid the price on the screen; re-pricing from the server's own history keeps the server authoritative without making an honest cashier short. |
| OF4 | **Shift.** Offline orders belong to the shift that was open on that device when the order was taken. The server refuses to close that shift while the device reports unsynced orders (owner force-close with a reason stays possible). Orders arriving after the shift was closed still go to their original shift; its Z-report is marked `amended` and keeps the original close figures. | The cash is already in that drawer; moving it to another shift would show a false variance on both. |
| OF5 | **Stock.** Sync never rejects for stock. Tracked stock may go **below zero**; such lines are flagged `oversold` and audited. The device blocks offline sale of items whose cached stock is 0. | The goods were handed over and paid for; a negative balance is the honest record and feeds the next stock count. |
| OF6 | **Numbering.** The device prints a local queue number with a device prefix (e.g. `X1-007`) and an "OFFLINE – official number follows" mark. On sync the server assigns the official `order_number`/`queue_number` from `store_daily_sequences` and keeps the offline number in its own searchable column. | Official numbers stay gap-free and server-issued. |
| OF7 | **Limits.** A device stops taking offline orders after **12 hours or 300 orders** since its last successful sync, whichever comes first. Both values live in `store_settings` per store. | One business day of outage; bounds fraud exposure and catalog staleness. |
| OF8 | **Static QRIS offline.** The cashier must enter the amount and the reference/time shown in the merchant app. The server stores the payment as `static_qris` verified by that cashier and flags it `offline_unreconciled` until matched against the bank/settlement record (audit ticket P2-02). | Consistent with K8. The reference makes a false claim traceable; prevention comes with settlement matching. |
| OF9 | **Failed validation never discards data.** A submission the server cannot accept (unknown product, revoked device, expired verifier, limits exceeded, malformed payload) is stored as received with status `rejected` and a reason, is excluded from revenue and stock, and waits for the owner to **accept** (at a stated price) or **reject** (with a reason); both are audited. A revoked device cannot submit anything new. | Money was taken; losing the record is worse than a review queue. |

## 3. Backend design (to be refined in implementation planning)

**Data (additive migrations):**

- `devices`: `store_id`, `id`, `name`, `prefix` (unique per store), credential hash, `registered_by`, `revoked_at`, `last_sync_at`, `last_seen_pending`.
- `device_pin_verifiers`: per device × staff, verifier material, `expires_at`; deleted on device revoke or staff deactivation.
- `catalog_versions`: per store, monotonically increasing version with an immutable JSONB snapshot of sellable prices (variants, modifier options, tax/service settings) written whenever pricing-relevant catalog data changes.
- `orders`: `origin` (`online`/`offline`), `client_order_id` (UUID from the device, unique per store), `device_id`, `offline_created_at`, `offline_queue_number`, `catalog_version`, flags `stale_catalog`, `price_delta`.
- `order_items`: `oversold` flag.
- `payments`: `offline_unreconciled` flag and the cashier-entered static QRIS reference.
- `offline_order_submissions`: raw payload, `client_order_id`, device, status (`accepted`/`rejected`/`resolved_accepted`/`resolved_rejected`), reason, resolver, timestamps.
- `store_settings`: `offline_max_hours` (default 12), `offline_max_orders` (default 300).
- Relax `chk_products_stock_quantity` and the stock part of `chk_modifier_options_stock` to allow negative balances (see §5).

**API:**

- Owner: register/rename/revoke devices; list and resolve `offline_order_submissions`.
- Device: fetch catalog snapshot + version and PIN verifiers while online; `POST /api/v1/pos/offline-orders/sync` with a batch of orders and PIN-failure events; report pending count (used by OF4).
- Sync is idempotent per `client_order_id`: a repeated order returns the original result and never creates a second order, payment, stock movement or cash movement.

**Tenant resolution:** the device credential follows the guest-cookie pattern (`<store_id>.<device_id>.<secret>`); the store id only selects the tenant for `database.WithTenantTx`, and the secret is verified against the stored hash inside that transaction. No new SECURITY DEFINER function is needed.

**Processing per order (one transaction):** verify device and limits → verify the cashier existed and was active → re-price from `catalog_versions` → assign the official number → attach to the original shift and drawer → record payment and cash movement (cash) or flagged static QRIS → move stock through the shared helper (may go negative) → write outbox `order.paid` (KDS receives it as a late ticket) and `audit_logs` `OFFLINE_ORDER_SYNCED`.

## 4. Consequences

- Positive: the cafe keeps selling during an outage, every rupiah taken offline ends up in a shift and in the reports, and the server stays authoritative for price, number, stock and shift.
- Negative: two new credential types on the device (device credential, PIN verifiers) widen the attack surface of a lost tablet; reports can change after a shift closes (`amended` Z-reports); catalog changes now write a snapshot; stock can be negative.
- The frontend must provide a durable local queue, the offline receipt and the local PIN check; the backend contract above is the boundary.

## 5. Items needing separate approval at implementation time

1. **Schema change**: relaxing the two stock CHECK constraints (OF5). It removes a guarantee other code may rely on; all readers of `stock_quantity` must be checked first.
2. **Permission**: a new owner-only permission for device management (proposed `device.manage`).
3. **Public contract**: the sync request/response shapes in `docs/api/openapi.yaml`.

## 6. Tests required

- Same batch synced twice → exactly one order, payment, cash movement and stock movement per `client_order_id`.
- Client-sent prices are ignored; a `catalog_version` older than 24 h is accepted and flagged.
- Shift close is refused while the device reports pending orders; a late order lands in the original shift and marks its Z-report `amended`.
- Stock goes negative and the line is flagged `oversold`.
- Revoked device, expired verifier, exceeded limits and an unknown product each produce a `rejected` submission; owner accept/reject is audited.
- Cross-tenant: a device credential of store A cannot submit to store B.
