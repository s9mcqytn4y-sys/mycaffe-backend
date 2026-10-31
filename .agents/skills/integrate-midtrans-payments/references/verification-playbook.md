# Verification Playbook

Use project-native commands first. If the project has no tests yet, add focused tests around payment safety before or with the implementation.

Refresh current Midtrans product/API details from `https://docs.midtrans.com/llms.txt` before verifying product-specific request or callback behavior.

## Unit And Contract Tests

- Signature generation:
  - Snap/Core notification signature uses exact payload strings.
  - BI-SNAP access-token signature, transactional signature, and notification verification are separate helpers.
  - Signed body equals sent body.
- Status mapping:
  - Success, pending, failure/cancel/expire, refund, chargeback if supported.
  - Fraud challenge/deny behavior.
  - Late provider updates cannot regress terminal local states.
- Payload builders:
  - Amount formatting and currency.
  - Merchant order id/reference id.
  - Item details/customer details where required.
  - Redirect/callback URLs.
- Webhooks:
  - Missing required fields.
  - Invalid signature.
  - Duplicate callback.
  - Out-of-order callback after paid/fulfilled/refunded.
  - Provider status lookup fallback if required.
- Account linking:
  - State/CSRF validation.
  - Missing auth code.
  - Binding response persistence.
  - Binding Inquiry before tokenized payment.
  - Missing wallet or PayLater option disables payment creation.

## Integration And Sandbox Smoke

For hands-on sandbox commands, webhook replay, status polling, and credential-safe test evidence, read [sandbox-interaction-helper.md](sandbox-interaction-helper.md).

- Create one sandbox transaction for each enabled method.
- Confirm the customer-facing recovery page after refresh.
- Trigger or simulate callback and verify order state updates once.
- Replay the same callback and verify no duplicate side effects.
- Poll status before method selection for Snap and handle not-found/pending semantics gracefully.
- Verify provider dashboard callback URLs are public HTTPS and do not redirect unexpectedly.
- Check production and sandbox credentials are never mixed.
- If sandbox credentials or dashboard access are unavailable, run local deterministic checks and state exactly which external proof is missing.

## Operational Checks

- Example env includes every required key.
- Typed env validation fails fast for missing required production keys.
- Deployment/secret manager wiring includes every production key.
- Logs include order id, merchant order id, provider reference, payment method, provider, result status, and request id.
- Logs omit secrets, auth headers, signatures, access tokens, customer authorization tokens, cookies, and full unrestricted payloads.
- Rate limit or idempotency prevents rapid duplicate payment creation for the same order/user.
- Cancellation and expiry paths reconcile with provider state before destructive local changes when feasible.

## Handoff Checklist

Report:

- payment methods tested and their provider owner,
- exact sandbox transactions or simulated fixtures used,
- commands run,
- callback URLs configured or still requiring dashboard action,
- merchant activation prerequisites still pending,
- known production smoke steps that require real credentials or dashboard access.
