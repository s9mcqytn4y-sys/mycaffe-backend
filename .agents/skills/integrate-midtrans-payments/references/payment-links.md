# Payment Link Playbook

Use this when the merchant wants to collect payment without building a checkout — invoicing, social commerce, chat-based collection, or ad-hoc requests where a shareable URL is enough.

Always refresh current docs from `https://docs.midtrans.com/llms.txt`, especially Payment Link overview, Create Payment Link API, and HTTP Notifications. For sandbox smoke and webhook replay, use [sandbox-interaction-helper.md](sandbox-interaction-helper.md).

Before building Payment Link code, complete [merchant-readiness-preflight.md](merchant-readiness-preflight.md). Confirm sandbox keys, dashboard access, and which Payment Link creation channel the merchant wants (Dashboard UI vs API).

## Product Fit

Use Payment Link when:

- The merchant has no checkout app, or wants to collect payment outside the main app.
- A human operator generates the link (CRM, sales, support, finance) and shares it via email, WhatsApp, or chat.
- The flow is invoice-style — one customer, one amount, optional expiry — not a high-volume cart checkout.
- The merchant accepts a Midtrans-hosted page and does not need custom UI.

If the merchant has an app and wants embedded checkout, prefer Snap. If the merchant needs to collect repeating subscription payments, see classic Core API subscriptions documentation.

## Two Creation Channels

| Channel | Use when |
| --- | --- |
| Dashboard UI (Payment Link menu) | Operator-driven, no engineering effort, link generated manually |
| Create Payment Link API | App-driven, link is generated server-side from an order/invoice record |

This skill guides API integration. Dashboard usage is a documentation pointer, not code.

## Backend Link Creation

Server-side only:

- Sandbox endpoint: `https://api.sandbox.midtrans.com/v1/payment-links`
- Production endpoint: `https://api.midtrans.com/v1/payment-links`
- Auth: `Authorization: Basic base64(serverKey + ":")` — same Snap/Core server key.

Core request fields:

- `transaction_details.order_id` — unique per link.
- `transaction_details.gross_amount` — integer IDR for `FIXED_AMOUNT`; do not rely on it for `DYNAMIC_AMOUNT`.

Optional body fields worth knowing:

- `usage_limit` — maximum successful/paid transactions allowed. Current API docs list a default, but reusable links should always set an explicit value.
- `customer_required` — set `true` to force buyer to enter customer details on the page; otherwise details can be optional.
- `expiry.start_time`, `expiry.duration`, `expiry.unit` — sets when the link expires. If `start_time` is omitted, current docs say transaction time is used.
- `item_details` — line items shown on the hosted page; net total must equal `gross_amount` for `FIXED_AMOUNT`.
- `customer_details` — pre-fills buyer info on the page.
- `enabled_payments` — restrict methods (e.g., only BCA VA + GoPay).
- `callbacks.finish` — where to redirect the customer after payment.
- `payment_link_type` — `FIXED_AMOUNT` by default, or `DYNAMIC_AMOUNT` when the customer can enter the amount. For `DYNAMIC_AMOUNT`, configure `dynamic_amount` limits and do not rely on `gross_amount` or `item_details`.

Successful response returns:

- `order_id` — echoes the request.
- `payment_url` — the customer-facing URL to share.

Persist `payment_url`, the Payment Link `order_id` or `payment_link_id`, expiry, `usage_limit`, and the merchant invoice/order linkage **before** sending the link to the customer. Do not derive order identity from the URL alone at notification time.

## Webhook And Status Handling

Payment Link uses the same notification format as Snap and Core API. Reuse the existing dispatcher and signature verifier:

```text
SHA512(order_id + status_code + gross_amount + serverKey)
```

`scripts/verify_snap_signature.sh` works unchanged for Payment Link notifications.

Status rules and idempotency are identical to Snap:

- `settlement` / `capture` → paid (verify `fraud_status == "accept"` when present).
- `pending` → awaiting customer.
- `expire` → link expired before payment.
- `cancel` → operator-cancelled via dashboard or API.
- Notifications can duplicate; processing must be idempotent.

For reusable links (`usage_limit > 1`), reconcile each successful payment as a distinct purchase. Persist transaction-level identifiers such as `transaction_id`, provider `order_id`, `gross_amount`, and `transaction_time`; use `transaction_id` or the transaction-specific provider order id for idempotency instead of treating the Payment Link id alone as the paid order.

## Common Gotchas

- **Do not rely on the `usage_limit` default**. Current docs list a default, but merchant intent is clearer and safer when reusable links set an explicit limit.
- **Fixed vs dynamic amount**: `FIXED_AMOUNT` locks the amount; `DYNAMIC_AMOUNT` lets the customer input an amount within configured limits. Pick deliberately.
- **Expiry semantics**: `expiry.duration` is relative to `start_time`, not the moment the link is generated. Omit `start_time` to default to "now".
- **`enabled_payments` requires dashboard activation**. A merchant-disabled method passed in `enabled_payments` returns 4xx.
- **Cancellation is operator-driven**: the customer cannot cancel a Payment Link from the page. Provide an out-of-band cancel path (admin route or dashboard).

## Payment Link Production Checklist

- Sandbox and production server keys separated; sandbox URLs not used in production payment workflows.
- Production Payment Notification URL configured for the same merchant account; reuse Snap webhook handler.
- For each `enabled_payments` value: production dashboard activation confirmed.
- Operator runbook exists for: regenerating a lost link, cancelling a link, marking an offline-paid invoice as paid.
- Reusable-link transactions reconcile by transaction-level identifiers, not only by the Payment Link id.
- Link delivery channel (email, WhatsApp) preserves the URL exactly — no trailing whitespace, no URL-shortener that breaks the redirect chain.
