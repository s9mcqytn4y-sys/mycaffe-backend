# Sandbox Interaction Helper

Use this when the merchant asks the agent to prove a Midtrans integration in sandbox, diagnose sandbox failures, replay a webhook, poll provider status, or prepare evidence before go-live.

This is a skill-phase helper, not an MCP tool. The agent may generate commands and fixtures for the developer to run, or run them only when the developer has already provided safe sandbox credentials through the project's normal secret mechanism. Do not ask for production secrets in chat. Do not persist credentials in generated files, command history, logs, screenshots, or tickets.

Before any provider call, complete [merchant-readiness-preflight.md](merchant-readiness-preflight.md). Sandbox proof requires a Midtrans sandbox account/MID, dashboard access when callbacks are involved, sandbox credentials loaded through env/secrets, and payment methods active for the flow being tested.

## Contents

- Safety Rules
- Sandbox Evidence To Capture
- Snap Sandbox Smoke (with checklist)
- Snap Webhook Replay
- BI-SNAP Sandbox Smoke (with checklist)
- QRIS And Virtual Account Checks
- GoPay Tokenization And GoPayLater Checks
- Local-Only Fallbacks
- Output Format
- Tooling (scripts and fixtures)

## Safety Rules

- Confirm the merchant has sandbox account access and the requested methods are active before promising provider-level proof.
- Confirm the target environment is sandbox before any live provider call.
- Prefer environment variables and existing secret loaders over inline keys.
- Refuse to run live or production-key payment creation as a test. Offer a sandbox equivalent or a dry-run/local verification.
- Redact server keys, client secrets, private keys, access tokens, customer authorization tokens, auth codes, payment option tokens, signatures, cookies, and unrestricted provider payloads.
- Keep generated examples copy-safe: use placeholders like `$MIDTRANS_SERVER_KEY`, not real values.
- When credentials are unavailable, still verify local payload builders, signature code, webhook mapping, idempotency, env wiring, and documentation links.

## Sandbox Evidence To Capture

For every sandbox interaction, capture:

- command or project test that was run,
- environment and base URL,
- account/MID or dashboard environment confirmed without exposing secrets,
- merchant order id and local order id,
- provider reference or transaction id,
- payment method,
- request result code/message,
- local order/payment state before and after callback/status polling,
- callback URL configured or still pending in dashboard,
- logs checked and redaction result.

Never capture raw credentials or full customer PII.

## Snap Sandbox Smoke

Use current docs from `https://docs.midtrans.com/llms.txt` before issuing requests. Relevant docs usually include Snap Integration Guide, Request Body parameters, Snap JS, HTTP(S) Notifications, Transaction Status, Testing Payment on Sandbox, and Switching to Production Mode.

Copy this checklist into your sandbox run and tick items as you complete them:

```text
Snap sandbox smoke:
- [ ] Step 1: Generate a unique merchant order id
- [ ] Step 2: Create a Snap token from the backend (sandbox endpoint)
- [ ] Step 3: Open the chosen Snap display path (popup / redirect / embed)
- [ ] Step 4: Exercise Get Status before method selection; not-found is OK
- [ ] Step 5: Complete one sandbox payment method end-to-end
- [ ] Step 6: Verify the notification signature and update local order state
- [ ] Step 7: Replay the same notification and confirm idempotency (no double fulfillment)
- [ ] Step 8: Poll status and confirm local mapping matches provider state
```

**Step 1: Unique merchant order id**

Use a fresh id per attempt; the same id cannot be reused once a transaction exists in Midtrans.

**Step 2: Backend Snap token creation**

Call the merchant app's existing token route when one exists. Falls back to raw curl only if the app route is not yet wired.

**Step 3: Snap display**

Use the display mode the merchant ships in production (popup / redirect / embed). Test the path customers will see, not a different one.

**Step 4: Pre-method-selection status lookup**

Snap returns 404/not-found before the customer picks a method. Treat this as "not attempted yet," not a fatal failure.

**Step 5: Complete a sandbox payment**

Use Midtrans's published sandbox test cards / VA numbers / e-wallet credentials for the method you are testing.

**Step 6: Verify and update**

Confirm the merchant handler verifies signature with the production-style code path, not a relaxed sandbox path.

**Step 7: Replay for idempotency**

Use `scripts/replay_snap_webhook.sh --fixture assets/fixtures/snap-notification-settlement.json` to repost. The second call must not double-fulfill.

**Step 8: Status polling**

Confirm the merchant's status route can recover order state even if no notification arrived.

Copy-safe token request shape:

```bash
MIDTRANS_AUTH="$(printf '%s:' "$MIDTRANS_SERVER_KEY" | base64)"
ORDER_ID="sandbox-$(date +%s)"

curl -sS https://app.sandbox.midtrans.com/snap/v1/transactions \
  -H "Authorization: Basic $MIDTRANS_AUTH" \
  -H "Content-Type: application/json" \
  -d '{
    "transaction_details": {
      "order_id": "'"$ORDER_ID"'",
      "gross_amount": 10000
    },
    "customer_details": {
      "first_name": "Sandbox",
      "email": "sandbox@example.test"
    },
    "enabled_payments": ["credit_card"]
  }'
```

If the merchant app already has a token route, prefer exercising the app route instead of raw curl so order persistence, ownership checks, logs, and expiry are tested.

## Snap Webhook Replay

Use webhook replay to test the merchant's handler even when dashboard callbacks are not configured yet.

1. Build a fixture with the exact `order_id`, `status_code`, `gross_amount`, and `transaction_status`.
2. Compute signature with `SHA512(order_id + status_code + gross_amount + serverKey)`.
3. Post to the local or public webhook route.
4. Verify order state changed once.
5. Replay the same payload and verify no duplicate fulfillment.
6. Change the signature and verify rejection.

Copy-safe signature helper:

```bash
ORDER_ID="ORDER-local-123"
STATUS_CODE="200"
GROSS_AMOUNT="10000.00"
TRANSACTION_STATUS="settlement"
SIGNATURE="$(printf '%s' "${ORDER_ID}${STATUS_CODE}${GROSS_AMOUNT}${MIDTRANS_SERVER_KEY}" | shasum -a 512 | awk '{print $1}')"
```

Keep `gross_amount` as the provider string used in the payload. Do not parse and reformat it before hashing. Snap notification amounts arrive as the provider-formatted string (often `"10000.00"` for IDR 10,000); use the exact bytes that came in the payload, not a re-stringified number.

## BI-SNAP Sandbox Smoke

For BI-SNAP, prove signing and callback behavior separately before broad method testing.

Copy this checklist into your BI-SNAP sandbox run:

```text
BI-SNAP sandbox smoke:
- [ ] Step 1: Load sandbox BI-SNAP credentials from env/secrets only
- [ ] Step 2: Request or reuse a B2B access token (with expiry buffer)
- [ ] Step 3: Sign the exact byte string of the request body (scripts/sign_bisnap_transaction.py to cross-check)
- [ ] Step 4: Create one sandbox transaction per enabled method (QRIS / VA / one-time DD / tokenized GoPay)
- [ ] Step 5: Persist provider reference, instructions, expiry, and safe metadata
- [ ] Step 6: Verify callback signature handling with valid AND invalid fixtures
- [ ] Step 7: Poll status and compare provider mapping with local state
- [ ] Step 8: Replay callbacks (including stale statuses after paid) to confirm monotonic state
```

BI-SNAP signing dry-run checks:

- timestamp format and timezone match current docs (typically `YYYY-MM-DDTHH:mm:ss+07:00`),
- endpoint path excludes scheme/host/query unless docs say otherwise,
- body hash is lowercase-hex SHA-256 of the exact byte string sent,
- access-token (asymmetric RSA-SHA256), transaction (HMAC-SHA512 base64), and notification (RSA verify) signatures use separate helpers,
- external id/reference uniqueness works across retries,
- `partnerServiceId` is left-padded to 8 characters before being placed in the VA payload,
- when `seamlessData` is part of an account-linking request, the data is `encodeURIComponent`-wrapped before RSA-SHA256 signing,
- `Authorization-Customer: Bearer <customer_authorization_token>` is present for tokenized Direct Debit and absent for one-time Direct Debit.

If a sandbox provider call fails, preserve the response code/message and the signed endpoint path, but redact all secrets and authorization headers.

## QRIS And Virtual Account Checks

QRIS:

- QR content or display URL is persisted, not only kept in memory.
- Customer can refresh or reopen the order and still see QR instructions.
- Expiry is visible and status polling handles pending/paid/expired.
- Notification lookup can find the order by partner reference or provider reference.

Virtual Account:

- VA number, bank, provider reference, amount, and expiry are persisted.
- Payment instruction UI is generated from persisted state.
- Partner service id/customer number formatting follows current docs.
- VA notification payload maps to the correct local order.

## GoPay Tokenization And GoPayLater Checks

Run these only when the merchant has sandbox activation for tokenization/PayLater.

1. Start account linking with a CSRF/state value.
2. Complete return handling and verify state before binding.
3. Store the customer authorization token server-side only.
4. Run Binding Inquiry and persist current payment options.
5. Before payment, run Binding Inquiry again.
6. For wallet, require active `GOPAY_WALLET`.
7. For GoPayLater, require active `PAY_LATER` plus merchant activation flag.
8. Create Direct Debit with `Authorization-Customer`.
9. Test unlink and account-linking/unlinking notification reconciliation.

If PayLater is missing from inquiry, the correct result is disabled UI or a clear unavailable state, not a fallback to one-time GoPay.

## Local-Only Fallbacks

When sandbox credentials, dashboard access, or public callback URLs are unavailable, do not claim end-to-end verification. Instead run deterministic local checks:

- payload validation for amount, order id, item totals, and enabled methods,
- signature unit tests with known fixtures,
- webhook replay against local handler,
- status mapping and stale update tests,
- env/drift checks for all required keys,
- log redaction checks,
- route and callback URL inspection,
- pending payment recovery page checks.

Report the missing external proof explicitly.

## Output Format

When reporting sandbox work, use:

```text
Sandbox result:
- Product/method:
- Project route or command:
- Provider endpoint/base URL:
- Provider reference:
- Local order/payment state before:
- Local order/payment state after:
- Callback/status evidence:
- Idempotency evidence:
- Redaction checked:
- Remaining dashboard/activation steps:
```

## Tooling

The skill bundles deterministic helpers under `scripts/` and redacted webhook shapes under `assets/fixtures/`. Use the script that matches the failure you are diagnosing, not all of them.

| When | Use |
| --- | --- |
| Verify a Snap notification signature against a bundled payload | `MIDTRANS_SERVER_KEY=SB-Mid-server-test scripts/verify_snap_signature.sh --payload assets/fixtures/snap-notification-settlement.json` |
| Replay a Snap notification against the merchant's local webhook | `scripts/replay_snap_webhook.sh --target-url http://localhost:3000/api/payment/webhook --fixture assets/fixtures/snap-notification-settlement.json` |
| Compute the BI-SNAP transactional `X-SIGNATURE` for a request body | `scripts/sign_bisnap_transaction.py --method POST --path /v1.0/qr/qr-mpm-generate --body-file body.json` |
| Compute the BI-SNAP access-token `X-SIGNATURE` (asymmetric) | `scripts/sign_bisnap_access_token.py` |
| Verify a BI-SNAP notification signature | `scripts/verify_bisnap_notification.py --path /api/bisnap/notify --timestamp ... --signature ... --body-file payload.json` |
| Compare every BI-SNAP artifact (body hash, headers, signature) without sending | `scripts/dry_run_bisnap_sign.py --method POST --path /v1.0/... --body-file body.json` |
| Format `partnerServiceId` for VA payloads | `scripts/format_partner_service_id.sh 12345` |
| Emit a BI-SNAP-compatible Asia/Jakarta ISO-8601 timestamp | `scripts/bisnap_timestamp.py` |

Scripts that accept `MIDTRANS_SERVER_KEY` refuse production-looking keys unless `--allow-production` is passed. The bundled Snap fixtures are signed with `SB-Mid-server-test` for deterministic local checks; regenerate signatures with the merchant's sandbox key before replaying against real handlers. Redact secrets in any debug output. Read [scripts/README.md](../scripts/README.md) for the full safety model before running anything with real credentials.
