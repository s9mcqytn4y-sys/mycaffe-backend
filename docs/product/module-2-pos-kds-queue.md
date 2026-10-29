# Module 2: POS, KDS, Customer Display, and Queue

## Screens
- `/pos/cashier`: QR orders, walk-in orders, cash/QRIS checkout, quick stock toggle, shift lifecycle.
- `/pos/customer-display`: passive dual display for cart, total, QRIS, and payment confirmation.
- `/pos/kds`: large, high-contrast kitchen/barista tickets with station filtering and one-touch status actions.
- `/pos/tv-queue`: preparing/ready queue and optional promotional/menu display.

## Operational UX
- Design for peak-hour use: large touch targets, high contrast, clear item modifiers, low interaction count, visible queue count, and distinct but configurable alerts.
- KDS tickets must emphasize table/order number, dine-in/takeaway, quantities, and critical modifiers (e.g., less sugar, extra shot, allergy notes if supported).
- Use configurable aging thresholds rather than treating sample values (5/10 minutes) as universal. Measure paid-to-ready SLA per station and shift.
- Queue numbering must be generated safely under concurrency and be unique within the store and business date. Keep internal database IDs separate from human-readable queue numbers.
- Same-browser dual displays can use `BroadcastChannel`; separate devices require an authenticated backend WebSocket channel (`/ws` backed by `outbox_events`). Do not promise "0.01 second" delivery; measure it.
- Browser TTS/autoplay may be blocked until a user gesture. Provide visual fallback and a staff-controlled audio test/unmute action.
- Thermal printing may use browser print and 58/80 mm CSS initially. Direct USB/Bluetooth printing depends on device/browser/driver support and must be validated on the target hardware.

## Shift lifecycle
- Open shift with opening cash.
- Record cash and QRIS sales separately; record cash-out/petty cash with category, reason, actor, and timestamp.
- Close shift with a blind cash count, expected-vs-actual comparison, variance reason/approval, and an X/Z-style summary.
- Every sensitive action (void, refund, cash adjustment, stock toggle, price override, shift discrepancy) must be auditable.
- Cash drawer totals must exclude QRIS receipts from expected physical cash.

## Order and payment behavior
- Unpaid/expired orders must not enter the preparation queue unless a store-approved cash/manual workflow explicitly permits it.
- Payment status and preparation status are separate.
- Duplicate events must not create duplicate KDS tickets or queue announcements.
- A local offline screen does not imply that the database, other devices, or payment gateway is synchronized.

## Acceptance checks
- Test cashier and KDS on actual target screen sizes and touch devices.
- Test same-browser dual display and cross-device sync separately.
- Test queue-number concurrency, shift cash reconciliation, reconnects, printer fallback, and audio restrictions.
