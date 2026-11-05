# Competitor Analysis: majoo "Kasir Online"

Reviewed 2026-11-04 against <https://majoo.id/aplikasi-kasir-online> and the owner's feature list.
Status uses the four tiers of `AGENTS.md`. Release targets follow decision V2 in
[DecisionRegister.md](../Architecture/DecisionRegister.md).

## 1. What majoo sells

- **Platforms:** Android, iOS and Windows tablets; Windows and Android PCs; macOS. The apps are native.
- **Packages:** Advance at Rp 499.000 per outlet/month, Prime at Rp 999.000, plus Prime+ and a
  hardware Starter package. The local server, customer display and KDS/order display are Prime features.
- **Hardware:** receipt, kitchen and label printers (USB, Bluetooth or LAN); Bluetooth barcode
  scanner; cash drawer kicked on payment; customer display (dual screen on Windows); KDS or order
  display on a smart TV; Android EDC.
- **Architecture, as far as the page shows:** offline sales sync automatically when the device is
  online again. A "local server" mode lets one device act as master for clients on the same network,
  with an option to keep it always on, and a list of connected devices. majoo Teams, KDS and Order
  Display are separate apps.

The inferred design is a native client per device with a local database, a cloud sync service, an
optional LAN master that relays orders between devices while the internet is down, and per-device
peripheral drivers. MyCaffe takes the opposite default: a server-authoritative cloud API with thin PWA
clients. That keeps money logic in one place (`AGENTS.md`). Offline arrives in v0.10 as a narrow,
per-device path (ADR-005, V9).

## 2. Feature parity

| majoo capability | MyCaffe today | Target |
|------------------|---------------|--------|
| Four POS layouts, multi-device | Layout is a frontend concern; API serves POS, KDS, guest and owner | Frontend |
| Barcode/SKU scan | Variants have SKUs; no lookup by SKU or barcode | v0.10 with devices (`GET /pos/catalog?sku=`) |
| Receipt to the customer | Digital receipt link (B1) | v0.9 e-mail and WhatsApp Cloud API driver (W9); SMS link to verified phones (V4) |
| Offline sales, automatic sync | Not built | v0.10 per device (ADR-005) |
| Local server master/client | Not built; contradicts I2 | v0.10b on demand (V9) |
| Services workflow + "ready" e-mail | Not built | v0.12 |
| Petty cash, open/close cashier | Shifts, drawers, cash movements, X/Z reports | v0.8 outlet cash fund, daily limit, blind count (V13) |
| PIN or QR ID card | PIN with lockout | v0.8 QR badge + PIN (V14) |
| Online-delivery prices (Gojek/Grab) | Not built | v0.11 price lists per channel |
| Custom amount, negotiable price, minimum quantity | Not built | v0.11 |
| Order types (takeaway, delivery, table, service) | `dine_in`, `takeaway` | delivery in v0.11, service in v0.12 |
| Multi-payment | One payment per order or split | v0.8 (V12) |
| Rounding | None | v0.8 cash only, nearest (V11) |
| Tax and service on or off, promo tax | Per-store tax and service in bps | v0.11 with promotions (K7) |
| Invoice with due date and reminder | Not built | v0.11 |
| Void/refund with manager PIN or e-mailed token | Owner PIN, remote approval from the phone (ADR-007) | Done; discount approval in v0.8 |
| Partial refund per product; drawer or outlet cash | Full refunds per payment (RF1-c); by-item split refunds | v0.8 partial lines + outlet cash source; RF1-a/b/d/f |
| X/Z reports | Implemented (4b, B2) | Done |
| Table layout, status, move/merge/split, timer | Tables, sessions, board, QR | v0.9 areas (`tables.section`), move/merge/split, warning timer (W5, W6) |
| Split by amount or by product | Equal-N and by-item splits (4c) | By-amount split in v0.8 with multi-payment |
| Kitchen/checker/receipt printers, copies, footnote, label sticker | ESC/POS 58/80 mm receipts | v0.9 print jobs per station, copies, footer, TSPL labels |
| KDS per item | KDS tickets per station (5d) | Done |
| Order display TV with media | Not built | v0.9 queue display endpoint + WS `queue` |
| Self order with e-wallet | QR pay-first with Midtrans QRIS (5b) | Done; promotions in v0.11 |
| Dynamic QRIS, e-wallets, EDC | Midtrans Core API QRIS, gopay, shopeepay; EDC recorded manually | Done |
| Customer display | Not built | v0.10 with devices, reuses the `pos` channel |
| Menu books (breakfast/lunch menus) | Categories | v0.11 time-based menu books |
| Shift schedules, attendance (majoo Teams) | Out of scope (V10) | — |

## 3. Pain points majoo solves that MyCaffe must also solve

1. **Cashier speed at peak:** scanning, QR badge login and multi-payment without retyping. Covered by v0.8 and v0.9.
2. **Cash honesty:** blind count, petty cash with a limit, approval for refund, void and discount. Covered by v0.8.
3. **Kitchen never misses an order:** routing to station printers, KDS, and a printer fallback when offline. Covered by v0.9 and v0.10.
4. **The customer waiting:** order display TV and receipt messages. Covered by v0.9.
5. **Delivery margins:** channel prices that absorb the platform fee. Covered by v0.11.

Where MyCaffe can differentiate:

- **Remote approval** from the owner's phone with the owner's own PIN, bound to the exact amount (ADR-007). majoo offers a PIN or an e-mailed token.
- **Server-recomputed totals** with quote fingerprints. A tampered client cannot change a price.
- **Append-only cash ledgers** and an audit row for every override.
- **A single-VPS cost model** (ADR-002): a fraction of a per-outlet subscription.

## 4. Business schema (target state after v0.12)

New tables per release. Every table is RLS-scoped by `store_id` and uses composite foreign keys
`(store_id, id)` like migrations 000022–000024.

| Release | Tables | Purpose |
|---------|--------|---------|
| v0.8 | `payment_refund_lines` (and the one-refund-per-payment unique key relaxed to a running total) | Partial refunds per item |
| v0.8 | `orders.rounding_amount`, `payments.tender_seq` | Rounding, multi-payment order |
| v0.8 | `outlet_cash_movements`, `outlet_expense_categories` | Outlet cash fund (V13) |
| v0.8 | `staff_badges` | Revocable QR identity tokens (V14) |
| v0.8 | `otp_challenges`, `customers` (phone, verified_at) | SMS OTP for PIN reset and customer verification (V4) |
| v0.8 | `message_outbox` (channel `sms`, later `whatsapp`) | One delivery queue for SMS/WA, like `email_outbox` |
| v0.8 | `outbox_events.dead_at`, `attempts` and purge jobs | P0-08 |
| v0.9 | `table_session_links` (merge), `table_timers`, `display_tokens`; areas reuse `tables.section` | Merge/split, timers, queue TV |
| v0.9 | `printers`, `print_jobs` (station, copies, footer, format `escpos` or `tspl`), `receipt_deliveries` | Print routing, checker, label sticker, digital receipts |
| v0.10 | `devices`, `device_pin_verifiers`, `offline_batches`, `offline_orders` | ADR-005 offline sync |
| v0.11 | `sales_channels`, `channel_prices`, `promotions`, `customer_ledgers` (points, deposit), `coupons`, `invoices`, `invoice_reminders`, `menu_books` | Channels, loyalty, invoices |
| v0.11 | `stock_ledger` (ADR-006) | Inventory |
| v0.12 | `service_orders`, `service_steps`, `service_notifications` | Services workflow with ETA alerts |

Source of truth stays in PostgreSQL. Every money-moving table is append-only, or is guarded by
state-machine `CHECK` constraints and triggers.
