# Midtrans Runtime Patterns

Start every task by checking `https://docs.midtrans.com/llms.txt`, then load the current docs for the specific payment product. This file captures stable patterns and integration lessons; it is not a replacement for current docs.

## Product Split

| Need | Usual product | Notes |
| --- | --- | --- |
| Hosted checkout with broad method coverage | Snap | Backend creates token/redirect URL; customer chooses method in Midtrans UI. |
| Custom card, bank transfer, e-money, or direct API payment | Core API | Use backend Basic Auth. Check PCI and 3DS/card requirements. |
| SNAP-standard custom QRIS, VA, Direct Debit, tokenized GoPay | BI-SNAP | Requires B2B access token, request signing, callbacks, and product activation. |
| Dashboard/no-code invoice | Payment Link/Invoicing | Prefer when merchant has no app-owned checkout. |

## Snap And Core API

- Use `Authorization: Basic base64(serverKey + ":")` from the backend.
- Never expose server key to browser/mobile clients.
- Snap token creation is not equivalent to an assigned payment method. Status lookup may return not found until the customer chooses and proceeds with a payment method.
- Treat Snap.js/browser callbacks as UX hints only. Before fulfillment, verify via webhook signature and/or server-side status API.
- Classic Midtrans notification signature uses SHA-512 over `order_id + status_code + gross_amount + serverKey`. Use the exact `gross_amount` string from the payload.
- Map `capture` with accepted fraud status and `settlement` to paid. Map deny/cancel/expire/failure to cancelled or failed. Map refund statuses to refunded. Keep project-specific order terminology explicit.

## BI-SNAP

- Access-token request uses asymmetric signing. Keep the private key in server secrets.
- Transaction requests use symmetric HMAC signing. Keep the client secret in server secrets.
- Notification verification may use a Midtrans public key. Store public key env names clearly and support rotation if the merchant uses aliases.
- Generate timestamps in the format and timezone required by the product docs. Avoid relying on local machine timezone defaults.
- Sign the exact HTTP method, endpoint path, access token, hashed request body, and timestamp required by the current docs. Stable JSON serialization matters.
- Cache access tokens with an expiry buffer. Handle token refresh races safely.
- Generate unique external ids/references per provider expectations. Avoid collisions during retries.
- Keep one dashboard-facing notification dispatcher if the dashboard only accepts one URL, then route internally by notification payload/type.

## QRIS And Virtual Account

- Persist QR content/image URL, VA number, bank, provider reference, and expiry.
- Display a recovery page for pending payments; do not rely on in-memory checkout state.
- Payment instructions should be generated from persisted payment state, not reconstructed from the cart.
- Expiry can be enforced locally, but still reconcile with provider status because customers may complete near boundary times.

## GoPay One-Time, Tokenization, And GoPayLater

- Separate one-time GoPay payment from tokenized GoPay wallet payment.
- Account linking is its own state: get auth code, user authorization, binding, stored customer authorization token, binding inquiry.
- Store the durable customer authorization token securely. Do not return it to the client.
- Call Binding Inquiry before tokenized payment. Payment option tokens can change.
- Tokenized wallet payment should use the active wallet payment option. GoPayLater should use the active PayLater option and remain disabled unless merchant activation and account capability are verified.
- Preserve CSRF/state validation on account-linking return URLs.
- Do not whitelist exact GoPay redirect URLs unless current docs require it; redirection URLs can change.

## Callback And Redirect URLs

- Dashboard notification URLs must be publicly reachable. Do not use localhost, VPN-only hosts, basic-auth-protected URLs, or non-standard ports for production callbacks.
- Avoid accidental 301/302/303 responses on notification endpoints. They can change retry behavior or cause lost notifications.
- Validate trailing slash behavior for return URLs. Some redirect flows append query parameters and can be sensitive to framework redirects.
- Keep finish/unfinish/error redirect URLs separate from server-to-server payment notification URLs.

## Security And Data Handling

- Redact server key, client key if sensitive in the merchant context, private key, client secret, access token, authorization-customer token, signatures, cookies, and full customer PII.
- Store raw provider payloads only when useful for reconciliation and after redaction or allowlisting.
- Fulfillment must require verified provider status, not a frontend event.
- Make callback processing replay-safe. Store provider transaction id/reference and ignore duplicate events that do not advance state.

## Useful Current Docs

- Midtrans agent index: https://docs.midtrans.com/llms.txt
- Built-in Interface (Snap): https://docs.midtrans.com/docs/snap
- Snap Integration Guide: https://docs.midtrans.com/docs/snap-snap-integration-guide
- Snap JS: https://docs.midtrans.com/reference/snap-js
- Snap request body parameters: https://docs.midtrans.com/reference/request-body-json-parameter
- Request headers: https://docs.midtrans.com/reference/request-headers
- HTTP(S) notifications/webhooks: https://docs.midtrans.com/docs/https-notification-webhooks
- BI-SNAP security architecture: https://docs.midtrans.com/reference/bi-snap-security-architecture
- BI-SNAP signature generation: https://docs.midtrans.com/reference/signature-generation
- QRIS MPM: https://docs.midtrans.com/reference/mpm-api-qris
- GoPay/ShopeePay/Dana Direct Debit: https://docs.midtrans.com/reference/direct-debit-api-gopay
- GoPay tokenization: https://docs.midtrans.com/reference/direct-debit-api-gopay-tokenization
- Account Linking API: https://docs.midtrans.com/reference/account-linking-api
- Get Auth Code API: https://docs.midtrans.com/reference/get-auth-code-api
- Binding Inquiry API: https://docs.midtrans.com/reference/binding-inquiry-api
