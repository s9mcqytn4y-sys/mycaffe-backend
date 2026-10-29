# Module 1: Customer QR Ordering

## Product goal
Mobile-first, guest checkout with minimal friction. Target: a typical customer can choose items and start payment in under 90 seconds, without creating an account. Treat this as a measurable target, not a guarantee on every network/payment app.

## Primary flow
1. Scan table QR; validate the signed/opaque table token server-side.
2. Browse searchable, categorized menu; show unavailable products/modifiers clearly.
3. Configure required modifier groups and optional add-ons; allow item-specific notes.
4. Review cart, customer display name, dine-in/takeaway, optional WhatsApp/email, and configured fees/taxes.
5. Create a pending order/payment server-side and show the applicable QRIS payment instructions.
6. Display a public order tracker with only the minimum information authorized for that order.

## UX requirements
- Mobile-first, target content width around 480px; visible store logo/name/status and table identity.
- Sticky category navigation, compressed product descriptions, responsive images (WebP/AVIF where supported), accessible touch targets, and clear loading/error states.
- Variant/modifier selections must be attached to each cart line. Two identical products with different add-ons/notes must remain distinguishable in cart, checkout, kitchen ticket, and receipt.
- The server recalculates all prices and validates availability at checkout. The client total is only a preview.
- Keep guest checkout; customer name is required only if the store policy requires it, and contact details remain optional.
- Retain a non-sensitive active-order reference for recovery after closing the browser. The public status endpoint must not expose other orders or personal data.
- QR token/table identity must be validated server-side; browser `sessionStorage` is not a security control.
- Test whether each target payment app can import a saved QR image. Offer a clear fallback if it cannot.
- Clear only the submitted cart after a confirmed order-creation response; do not erase an unsubmitted cart on a transient failure.

## Payment and split-bill scope
- Distinguish static store QRIS/manual reconciliation from dynamic order-linked QRIS.
- Confirm payment through a verified provider notification or authorized cashier workflow, never via a client redirect.
- Split bill is a supported product requirement, but implement it only after the single-payer path is stable. Define equal split and item-based split, rounding, partial payment, cancellation, refund, and reconciliation semantics before coding.

## Edge cases
- Missing/invalid table token; inactive table; takeaway path.
- Product or modifier becomes unavailable while cart is open.
- Duplicate submission, retry, network timeout, expired payment, and repeated webhook.
- Two guests ordering from the same table on separate devices.
- Customer closes/reopens the browser or scans the QR again.
- Store is closed, QR ordering is paused, or the order queue is overloaded.

## Acceptance checks
- Menu performance measured on a realistic mobile connection and device.
- Server-side price/stock/modifier validation and idempotent order creation.
- Correct QRIS amount/status handling for the chosen payment mode.
- Realtime tracker reconnect behavior tested if realtime is enabled.
- Cart behavior tested on mobile browsers, including failures and retries.
