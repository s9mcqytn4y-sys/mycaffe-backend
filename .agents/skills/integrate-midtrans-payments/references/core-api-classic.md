# Classic Core API Playbook

Use this when the merchant wants a custom payment UI built on the **classic (non BI-SNAP) Midtrans Core API** — typically for cards, OTC (Alfamart/Indomaret), and legacy Virtual Accounts. For QRIS, e-wallet, and modern direct-debit flows, route to BI-SNAP instead.

Always refresh current docs from `https://docs.midtrans.com/llms.txt`, especially Core API overview, Card Payment, Tokenization, 3DS, BIN API, OTC payments, and Virtual Account legacy pages. For sandbox smoke and signature checks, use [sandbox-interaction-helper.md](sandbox-interaction-helper.md).

Before building Core API code, complete [merchant-readiness-preflight.md](merchant-readiness-preflight.md). Confirm sandbox and production keys, PCI scope acceptance for cards, dashboard 3DS/Midtrans Lock activation, and the full method-by-method ownership map (this skill assumes Core API only for the methods listed below).

## Contents

- Product Fit
- Authentication
- Card Payment Flow
- BIN API
- Saved Card / One-Click / Subsequent
- OTC Payments
- Legacy Virtual Account
- Status Mapping
- Common Gotchas
- Core API Production Checklist

## Product Fit

Choose the classic Core API only when the merchant must own the customer-facing payment UI for:

- **Card payments** — full custom UI with `MidtransNew3ds` tokenization and 3DS, including saved-card / one-click / installment / pre-authorization.
- **OTC (Alfamart, Indomaret)** — custom payment-instruction page with the merchant brand.
- **Legacy bank Virtual Account** — when the merchant explicitly does not want BI-SNAP VA shape, e.g. legacy back-office reconciliation already built around Core API VA semantics.

Prefer Snap unless one of the bullets above is a real merchant requirement. PCI sensitivity and 3DS edge cases are easier when Snap owns the card UI.

## Authentication

Same Basic Auth as Snap:

- Sandbox endpoint root: `https://api.sandbox.midtrans.com`
- Production endpoint root: `https://api.midtrans.com`
- Header: `Authorization: Basic base64(serverKey + ":")`

The same server key works for Snap, classic Core API, Payment Link, and Refund. The two host families (`api.*` for Core API and `app.*` for Snap) must not be mixed — see [snap-checkout.md](snap-checkout.md).

## Card Payment Flow

Classic card payment is **frontend tokenization plus backend charge/status handling**:

1. **Frontend tokenization (`midtrans-new-3ds.min.js`, `MidtransNew3ds.getCardToken`)** — exchanges the customer's PAN, CVV, and expiry for a `token_id`. The merchant frontend never POSTs raw card data to the merchant backend. Only the `token_id` (and optionally the masked card number for UX) reaches merchant servers.
2. **Backend charge (`POST /v2/charge`)** — sends `payment_type: "credit_card"`, `transaction_details`, and `credit_card.token_id`. Returns either a 3DS redirect URL (`redirect_url`) when 3DS is enabled, or a final status (`settlement` / `capture`).
3. **Frontend 3DS** — opens the `redirect_url` in a popup or iframe. After the customer completes 3DS, the merchant frontend triggers a backend status check on `/v2/{order_id}/status`.

Charge payload essentials:

```json
{
  "payment_type": "credit_card",
  "transaction_details": {
    "order_id": "ORDER-12345",
    "gross_amount": 150000
  },
  "credit_card": {
    "token_id": "<token from MidtransNew3ds.getCardToken>",
    "authentication": true,
    "save_token_id": false
  }
}
```

Critical fields:

- `authentication: true` — enables 3DS. Keep enabled unless Midtrans and the merchant risk owners explicitly approve otherwise; some BIN-banks reject non-3DS.
- `save_token_id: true` — store the resulting `saved_token_id` for one-click / subsequent / recurring usage. Returned in the charge response under `credit_card.saved_token_id`.
- `installment_term` — required when offering installment; confirm current docs for bank/channel routing fields before exposing terms.
- `bank` — explicit acquirer routing (`mandiri`, `bca`, `bri`, `cimb`, etc.) when BIN routing is needed.

The same `/v2/charge` endpoint handles capture-later (`type: "authorize"`), saved-card/two-click/one-click charges using saved token values as `credit_card.token_id`, and pre-authorization. See current docs for exact field names before coding; card feature names have shifted across docs generations.

## BIN API

Use `GET /v1/bins/{bin}` server-side to look up issuer info before charge. Authenticate exactly as current docs require; do not expose a secret key in the browser. Useful for:

- Pre-charge issuer-driven UX (showing card-network logo, gating local-only methods).
- Pre-checking installment eligibility before exposing the term selector.
- Risk routing before tokenization.

Never expose the BIN API call from the customer browser unauthenticated; if you need browser-side BIN data, proxy it through a thin merchant endpoint with rate limiting.

## Saved Card / One-Click / Subsequent

For returning customers:

1. On first charge, set `credit_card.save_token_id: true`. Persist `credit_card.saved_token_id`, `credit_card.saved_token_id_expired_at`, and `credit_card.masked_card` against the customer profile when returned.
2. For two-click subsequent payments, retrieve a fresh `token_id` from `MidtransNew3ds.getCardToken` using the saved token plus customer CVV, then charge with that `token_id`.
3. For one-click or recurring charges, use the saved token as `credit_card.token_id` only when the merchant has the required recurring/one-click activation and current docs confirm the flow. The customer may not be asked for CVV, so document merchant risk acceptance.

Saved tokens have an expiry (typically aligned with the card expiry). Implement a refresh / re-tokenize flow.

## OTC Payments

Alfamart and Indomaret are over-the-counter cash payments. The flow is:

1. Backend charges with `payment_type: "cstore"` (Alfamart) or `payment_type: "cstore"` with `cstore.store: "indomaret"`.
2. Charge response returns a `payment_code` and an expiry. Persist both.
3. Customer-facing instruction page shows `payment_code` and the merchant name.
4. Customer pays at the convenience-store counter; merchant receives notification with `transaction_status: "settlement"`.

Charge payload example:

```json
{
  "payment_type": "cstore",
  "transaction_details": {
    "order_id": "ORDER-12345",
    "gross_amount": 50000
  },
  "cstore": {
    "store": "alfamart",
    "alfamart_free_text_1": "Thank you for your order",
    "alfamart_free_text_2": "Reference: ORDER-12345",
    "alfamart_free_text_3": "Show this code at the counter"
  },
  "custom_expiry": {
    "order_time": "2026-06-04 14:00:00 +0700",
    "expiry_duration": 24,
    "unit": "hour"
  }
}
```

OTC gotchas:

- Alfamart and Indomaret have different customer-facing branding rules. The free-text fields appear on the customer receipt; keep them brand-safe and free of secrets.
- Indomaret has a maximum amount per transaction (varies, typically ~5M IDR); validate before charge.
- Most OTC payments are **not refundable** through the API — the operator runbook must include a manual customer-resolution flow.

## Legacy Virtual Account

Classic Core API VA uses `payment_type: "bank_transfer"`:

```json
{
  "payment_type": "bank_transfer",
  "transaction_details": {
    "order_id": "ORDER-12345",
    "gross_amount": 100000
  },
  "bank_transfer": {
    "bank": "bca",
    "va_number": "1234567890"
  }
}
```

Charge response returns `va_numbers[]` containing the assigned VA number per bank. Persist them and present to the customer as bank-transfer instructions.

Use BI-SNAP VA instead unless the merchant has a documented reason to stay on classic Core API VA.

## Status Mapping

Classic Core API uses the same `transaction_status` and `fraud_status` fields as Snap notifications. The signature rule is identical:

```text
SHA512(order_id + status_code + gross_amount + serverKey)
```

`scripts/verify_snap_signature.sh` works for Core API notifications too. Status mapping:

- `capture` + `fraud_status: "accept"` → paid.
- `capture` + `fraud_status: "challenge"` → manual review pending; do not fulfill.
- `settlement` → paid (most non-card methods land here directly).
- `pending` → awaiting customer.
- `deny` / `cancel` / `expire` / `failure` → terminal failure.
- `authorize` → authorized only; capture later via `/v2/{order_id}/capture`.

## Common Gotchas

- **Sending CVV to merchant backend**: huge PCI scope expansion. Always tokenize on the frontend.
- **Forgetting `authentication: true`**: most BIN-banks reject non-3DS in production; sandbox may accept it and mislead.
- **Mixing saved-token flows**: two-click, one-click, and recurring use different token/CVV expectations. Confirm the current docs and merchant activation before charging a saved token.
- **Mixing `api.*` and `app.*` hosts**: 404 because Core API endpoints do not exist on the Snap host family.
- **OTC payment-code formatting**: the customer-facing code is the merchant's responsibility to display readably (e.g., grouped digits). Raw API output is a single string.

## Core API Production Checklist

- Sandbox and production server keys separated.
- Frontend uses Midtrans client key only; backend uses server key only.
- All card charges use `authentication: true` unless explicitly waived.
- Saved-card / one-click flows respect card expiry refresh.
- 3DS popup/iframe is implemented with backend status verification, not frontend trust.
- OTC `payment_code` is displayed exactly as returned; expiry is enforced both server-side and customer-facing.
- BIN API is proxied through merchant backend, never exposed unauthenticated to customer browsers.
- Webhook handler reuses Snap signature verifier and status mapping; idempotent and monotonic.
- Operator runbook covers OTC manual customer-resolution and card chargeback workflows.
