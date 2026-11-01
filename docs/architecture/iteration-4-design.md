# Iteration 4 Design: Modifiers, Cart, Open Tab & Split, Shift & Cash Drawer, Void & Refund

Owner decisions P1–P9 (sixth interview, 2026-10-31) plus earlier O1, O4, C1, C2, C3, T2, H1. Delivery
order: **PR 4a → 4b → 4c → 4d**; each PR ships an additive migration, PostgreSQL integration tests over
HTTP, OpenAPI, and docs, following [api-conventions.md](api-conventions.md).

## 1. Add-ons, sub-add-ons, variants & categories (PR 4a)

| Topic | Current | Decision |
| ------- | --------- | ---------- |
| Variants | ✅ own SKU/price (iteration 3) | sizes/temperature are variants, never modifiers |
| Modifier price | `price_delta >= 0` | **P1**: negative allowed ("Tanpa susu −Rp2.000"); unit price of a line (variant + all modifiers) must stay ≥ Rp0 |
| Sub-add-ons | 1 level (option → child group), enforced by pricing | kept at 1 level (P1) |
| Required groups | `is_required`, per-product `override_required` | kept |
| Quantity per option | `max_quantity`, single vs multiple, `min/max_select` | kept; CRUD validates `min ≤ max`, single ⇒ max 1 |
| Categories | flat | kept flat for the single-cafe MVP |

Modifier CRUD: groups (name, selection, required, min/max, order, active), options (name, delta, stock,
max quantity, order, availability), product↔group links (order, override required), option↔child-group
links (depth 1: a child group may not itself have child groups). Same 422/409/404 contract; option
deletion archives (orders snapshot option names/prices already).

Edge cases: required group whose options are all unavailable ⇒ product shown sold out; deleting a group
linked to products ⇒ 409 unless `force_unlink=true`; cycle (group as its own descendant) rejected; price
delta changes audited as `PRICE_CHANGE`.

## 2. Cart lifecycle & group order (PR 4a, P8)

```text
device cart (local storage) ──add/remove──► POST …/quote ──► priced lines + totals (nothing stored)
     │                                           ▲
     └── checkout ──► POST /pos/orders | guest order ─┘ re-priced server-side;
                                                       409 QUOTE_CHANGED if total/stock differs
                                                       from the client's expected_total
```

- No cart table. `quote` uses the same pricing engine as order creation (single source of truth).
- `expected_total` (optional) on order creation: mismatch ⇒ 409 with the fresh quote, so the customer
  display never shows one number and the receipt another.
- Group order: each guest device keeps its own cart; the table session (T1) groups orders for staff.
  Shared multi-device carts are deferred.

## 3. Shift & cash drawer (PR 4b, P6, P7, C1)

```text
            open (float, cashier|owner)            close (blind count)
 [no shift] ─────────────────────────► [open] ─────────────────────────► [closed]
                                          │  variance > tolerance ⇒ reason + owner PIN
                                          └─ owner force-close (reason, audited)
```

Tables: `cash_drawers` (one per POS terminal, name), `shifts` (drawer, opened_by, float, opened_at,
closed_by, counted_cash, expected_cash, variance, status), `cash_movements` (append-only: `sale`,
`refund`, `paid_out`, `cash_in`, `no_sale`, `float`; amount, actor, reason, ref).

Rules:

- One open shift per drawer; cash tender is rejected (409 `NO_OPEN_SHIFT`) without one. Non-cash tenders
  need no shift but are reported on the shift that was open on that terminal (if any).
- Expected cash = float + sales − refunds − paid-outs + cash-ins. QRIS/EDC never count as drawer cash.
- Blind close: the cashier submits the count before seeing the expected value; variance >
  `cash_variance_tolerance` ⇒ reason + owner PIN.
- Handover = close + open. Owner may force-close an abandoned shift.
- Open tabs at close must be transferred to the next shift or settled (from 4c).
- `single_auto` mode: one shift per business day, auto-opened by the first cash action with float 0.
- Drawer kick (H1): responses of cash sale/refund/paid-out/cash-in/no-sale carry `open_drawer: true`;
  the frontend sends ESC/POS `ESC p` through the printer. `no_sale` requires a reason and appears on X/Z.
- X report (any time) and Z report (at close): totals per method, cash movements, voids, refunds,
  no-sales, discounts, variance.

Edge cases: two terminals opening the same drawer concurrently (row lock + unique open shift); shift
left open over midnight (business day = open date); power loss (shift persists; reopen continues);
negative expected cash (refund larger than drawer) ⇒ refused.

## 4. Open tab, rounds & split bill (PR 4c, P2, P3, T2)

```text
open tab (cashier) → round 1 (items → KDS, stock committed, prices frozen) → round n …
  → close tab → [optional split: equal N | by item] → pay each split (any method) → paid
```

- One tab = one order with `order_rounds`; adding items creates a round. Only cashier/owner may open tabs.
- Split modes (P3): **equal N** (remainder rupiah to split 1) and **by item** (tax/service allocated
  proportionally, rounding remainder to the last split). Splits are editable until the first split is
  paid, then locked.
- Payment settles **one split**; the order becomes `paid` when all splits are paid. This fixes risk R4
  (today `commitPaidOrderTx` marks all splits paid).
- QR guest orders stay pay-first per device (natural split).

Edge cases: void of an item already allocated to a split ⇒ splits recalculated if none paid, otherwise
refused (refund flow); tab with zero items cannot close; tab older than the business day flagged on Z.

## 5. Void & refund (PR 4d, P4, P5, O1, C2)

| | Void | Refund |
| -- | ------ | -------- |
| When | before payment (line or whole order) | after payment (line, partial qty, or whole) |
| Stock | ticket not started ⇒ restock; preparing/ready ⇒ `waste` movement | optional restock flag |
| Money | none | cash from the open drawer (needs open shift); non-cash: Midtrans API where supported, else manual transfer recorded |
| Authority | own PIN + reason; above `void_refund_owner_threshold` ⇒ owner PIN | same, plus time window: cashier same business day; owner ≤ 7 days |
| Limits | — | total refunds ≤ paid amount; refund method defaults to the original |
| Audit | `VOID_ORDER` | `REFUND` |

Reasons (structured): salah input, pelanggan batal, item habis, komplain, lainnya (free text required).
Paid orders are never voided. Refund updates `payment_status` (`partially_refunded`/`refunded`) separately
from `order_status`.

## 6. Decisions recorded

| ID | Decision |
| ---- | ---------- |
| P1 | Negative modifier deltas allowed; line unit price ≥ 0; sub-add-ons stay 1 level |
| P2 | Open tab = one order with rounds; each round goes to KDS with frozen prices |
| P3 | Split equal + by item; deterministic rupiah remainder; locked after first split payment |
| P4 | Void per item/order; restock vs waste by ticket status; structured reasons; O1 owner PIN |
| P5 | Refund: cashier same business day; owner up to 7 days; total ≤ paid |
| P6 | One drawer per terminal, one open shift per drawer; handover = close/open; owner force-close; `single_auto` kept |
| P7 | Drawer opens on cash transactions; `no_sale` needs a reason and is reported |
| P8 | Local cart + server quote endpoint; 409 on change at checkout; group order via table session |
| P9 | Iteration 4 plan approved; categories stay flat |
