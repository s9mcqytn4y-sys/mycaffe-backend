# BI-SNAP Core Playbook

Use this when the merchant asks for BI-SNAP, SNAP-standard Core API, QRIS MPM, virtual account, one-time Direct Debit, custom payment UI, BI-SNAP signatures, BI-SNAP notifications, BI-SNAP retry/expiry behavior, or BI-SNAP status reconciliation.

Always refresh current docs from `https://docs.midtrans.com/llms.txt`, especially BI-SNAP security architecture, signature generation, access token, QRIS MPM, Direct Debit, virtual account, notification setup and standard callback paths, per-product notification response codes, status APIs, and sandbox testing. For concrete sandbox smoke, signing dry-runs, status polling, and webhook replay, use [sandbox-interaction-helper.md](sandbox-interaction-helper.md).

## Contents

- Product Fit
- BI-SNAP Implementation Flow
- Required Project Boundaries
- Credentials And Environment
- Hosts
- Signing Model
- Payment State Model
- Payment Method Playbooks (QRIS MPM, Virtual Account, One-Time Direct Debit)
- Notification Routing And Response Contract
- Status Mapping
- Operational Guardrails
- Troubleshooting Matrix
- Production Checklist

## Product Fit

Use BI-SNAP when the merchant needs app-owned payment UI and is ready to own:

- B2B access token retrieval and caching,
- asymmetric and symmetric signatures,
- product-specific request payloads,
- one or more public notification URLs,
- status mapping and reconciliation,
- sandbox/live credential and certificate/key lifecycle.

If the merchant only wants a hosted payment page, prefer Snap.

## BI-SNAP Implementation Flow

1. Inspect existing order, payment, callback, repository, environment, logging, and test boundaries.
2. Confirm BI-SNAP readiness: account/MID, partner/channel/client ids, client secret, private key, Midtrans notification public key, activated products, registered notification URLs, and the proof level required.
3. Create or reuse the merchant's local order.
4. Obtain a cached B2B access token; refresh with an expiry buffer and guard refresh races.
5. Build the product-specific request payload server-side (QRIS MPM, VA, or one-time Direct Debit).
6. Move the local order/payment attempt into `creating_payment` (or equivalent) before calling Midtrans.
7. Sign the exact serialized request body and call the transactional host.
8. Persist provider reference, customer instructions (QR content/URL, VA number, deeplink/redirect), expiry, and safe request metadata.
9. Move the local order/payment attempt into `awaiting_payment`.
10. Render instructions from persisted payment state, not from a transient response held in memory.
11. Reconcile from a verified notification and/or trusted status lookup; treat any frontend return as a UX hint only.
12. Keep retry, cancellation, and expiry paths explicit, and keep notification handling idempotent and monotonic.

## Required Project Boundaries

Keep these concerns separate:

- Route/controller: auth, input validation, HTTP response.
- Domain/use-case: payment method routing, order state transitions, idempotency.
- Provider client: BI-SNAP headers, signing, fetch/SDK transport, provider payloads.
- Repository/model: payment/order persistence only.
- UI: method selection, instructions, redirect/open actions, status display.

Do not put BI-SNAP private keys, client secrets, access tokens, or `Authorization-Customer` values in browser code.

## Credentials And Environment

Exact names differ by merchant, but the integration usually needs:

- client id/client key,
- partner id,
- channel id,
- client secret,
- private key for access-token signing,
- Midtrans public key for notification verification when applicable,
- merchant id,
- product-specific merchant handles or service ids,
- sandbox/production base URL switch.

Wire new env vars through every environment surface: example env, typed validation, deployment secrets, CI/drift tests, and operator docs.

**Key-from-env gotcha**: private keys and the Midtrans notification public key are usually stored in environment variables, where they commonly arrive as a single line with literal `\n` escapes or as a base64-wrapped PEM. Normalize before use: unescape `\n`, base64-decode when there is no `BEGIN` header, and re-wrap the body to 64-character lines so `createPrivateKey`/`createPublicKey` accept the key. Centralize this in the signing helper, not in callers.

## Hosts

Two distinct hosts are used by BI-SNAP. Verify against current docs because Midtrans can introduce regional or product-specific variants:

| Use | Sandbox | Production |
| --- | --- | --- |
| Transactional BI-SNAP APIs (access-token, QRIS, VA, Direct Debit, status, binding, inquiry, unbind) | `https://merchants.sbx.midtrans.com` | `https://merchants.midtrans.com` |
| GoPay Get Auth Code (account linking only) | `https://merchants-app.sbx.midtrans.com` | `https://merchants-app.midtrans.com` |

The `merchants-app` host is only for the Get Auth Code redirect step. Binding, inquiry, unbind, and tokenized Direct Debit return to the regular `merchants` host.

GoPay account-linking and tokenized GoPay payment reuse this BI-SNAP access-token signing and the `merchants-app` host for the Get Auth Code step only. Do not implement that flow here — see [gopay-tokenization.md](gopay-tokenization.md).

## Signing Model

BI-SNAP uses **three independent signature helpers**. Never share one across the three. Confirm exact formats against current docs before shipping.

### Access token (asymmetric, B2B)

- Endpoint: `POST {host}/v1.0/access-token/b2b`.
- String-to-sign: `${clientId}|${timestamp}` (pipe-delimited), signed RSA-SHA256 with the merchant private key, base64-encoded.
- Headers: `X-CLIENT-KEY: ${clientId}`, `X-TIMESTAMP: ${timestamp}`, `X-SIGNATURE: ${signature}`.
- Body: `{"grantType":"client_credentials"}`.
- A success response carries a SNAP-standard response code (for example `2007300`) plus `accessToken` and `expiresIn`. Treat a non-success code as a hard failure.

### Transaction request (symmetric, per call)

- String-to-sign: `${httpMethod}:${endpointPath}:${accessToken}:${bodyHashHex}:${timestamp}`, where `bodyHashHex` is lowercase-hex SHA-256 of the exact serialized request body.
- Signed HMAC-SHA512 keyed on `clientSecret`, base64-encoded, and sent as `X-SIGNATURE` together with the `Authorization: Bearer ${accessToken}`, timestamp, partner-id, channel-id, and external-id headers required by current docs.

### Notification (verify inbound)

- Verify the inbound `X-SIGNATURE` over `POST:${requestPath}:${bodyHashHex}:${timestamp}` using the Midtrans public key (RSA-SHA256). `requestPath` is the exact registered callback path — see Notification Routing And Response Contract.

### Practical signing rules

- Generate timestamps in ISO-8601 with the Asia/Jakarta offset (for example `2026-05-27T14:30:00+07:00`); compute the offset explicitly, never from the machine timezone.
- Serialize the JSON body once, hash that exact byte string, sign, and send the same bytes. Do not let the HTTP client re-serialize, re-order keys, or change spacing after signing.
- Cache the B2B access token with an expiry buffer (for example refresh ~30 seconds early) and guard concurrent refreshes so parallel requests do not each mint a token.
- Keys often arrive `\n`-escaped or base64-wrapped from env; normalize before signing (see Credentials And Environment).

## Payment State Model

BI-SNAP needs the same explicit local state model as Snap. Look for or create equivalents of:

| Local state | Meaning | Provider action |
| --- | --- | --- |
| `selecting_method` | Customer has not requested payment yet | None |
| `creating_payment` | App has committed to creating a provider attempt | Charge request in flight |
| `awaiting_payment` | Provider accepted the charge; instructions are live | Notification/status decides final state |
| `paid` | Verified successful settlement | Fulfill |
| `failed` / `cancelled` / `expired` | Provider or local timeout ended the attempt | Show retry path |
| `refunded` / `partially_refunded` | Provider refund event accepted | Reconcile fulfillment/accounting |

Set `creating_payment` before the provider call so a failed charge never leaves the order in an unidentified state. Persist enough to resume after refresh:

- internal order id and provider reference (`originalReferenceNo`/`referenceNo` and `trxId`/partner reference),
- customer instructions: QR content or QR image URL, VA number plus bank, or deeplink/redirect URL,
- local expiry and provider expiry,
- latest provider status code,
- safe provider metadata for debugging.

Keep notification and status handling idempotent and monotonic: never let a late pending/cancelled update overwrite a paid, fulfilled, shipped, delivered, or refunded order unless the provider event is a valid refund/chargeback transition.

## Payment Method Playbooks

### QRIS MPM

- Create payment server-side.
- Current docs expose `/v1.0/qr/qr-mpm-generate` for the charge and `/v1.0/qr/qr-mpm-notify` for the QRIS MPM notification callback. Preserve each exact path: the transactional signature is computed over the generate path and the notification signature over the callback path, so shortening or normalizing either path breaks signing or verification.
- The generate response returns three QR fields: `qrUrl` (a downloadable QR image URL), `qrImage` (base64-encoded PNG), and `qrContent` (the raw QR string). Resolve the display image with a fixed priority rather than picking arbitrarily: `qrUrl` first (render directly, no local generation), then `qrImage` (wrap as `data:image/png;base64,` unless it already is), then `qrContent` (generate a QR locally as a fallback). If the frontend renders `qrUrl` via an `<img>`/image component, allow unoptimized/remote rendering so the Midtrans-hosted image is not re-encoded.
- Persist provider reference, QR content or QR image URL, expiry, amount, and method.
- Render QR/instructions from persisted payment state.
- Poll or reconcile via status API while waiting for notification.

### Virtual Account

- Build bank-specific VA payloads from current docs.
- Persist VA number, bank, expiry, provider reference, and amount.
- Show a recovery page for pending payments.
- Reconcile expiry locally and with provider status.

**`partnerServiceId` formatting gotcha**: the field must be exactly 8 characters in the create-VA payload. Numeric service ids shorter than 8 digits must be **left-padded with spaces** to 8 characters (e.g., `"   12345"`). Sending the raw, unpadded value returns a 400 with a non-obvious validation error. Centralize the padding logic in the provider client, not in callers.

**`customerNo` and `virtualAccountNo` relationship**: `virtualAccountNo` is typically `partnerServiceId + customerNo`. Status callbacks may report `partnerServiceId`, `customerNo`, and `virtualAccountNo` separately; reconciliation should join on `trxId` (the order id) as the primary key, not the VA number.

### One-Time Direct Debit

- Do not confuse one-time GoPay/ShopeePay/DANA Direct Debit with GoPay tokenized wallet.
- Persist redirect/deeplink URL, provider reference, expiry, and status payload.
- Treat frontend return as a UX hint; verify via status/notification.

**Header contrast with tokenized Direct Debit**:

| Header | One-time Direct Debit | Tokenized GoPay Direct Debit |
| --- | --- | --- |
| `Authorization: Bearer <accessToken>` | ✓ | ✓ |
| `X-SIGNATURE` (transactional HMAC) | ✓ | ✓ |
| `Authorization-Customer: Bearer <customer_authorization_token>` | ✗ must be absent | ✓ required |
| `chargeToken` field in body | access token | customer authorization token |

Sending `Authorization-Customer` on a one-time charge or omitting it on a tokenized charge causes the provider to reject the request. See [gopay-tokenization.md](gopay-tokenization.md) for the tokenized flow.

### Per-Product Reconciliation Keys

Each product reports status in a different field and reconciles on a different key. Confirm against current docs, but the shape is:

| Product | Status field in callback | Reconcile by |
| --- | --- | --- |
| QRIS MPM | `latestTransactionStatus` | `originalPartnerReferenceNo` (merchant order id) or `originalReferenceNo` (provider ref) |
| Virtual Account | `additionalInfo.paymentFlagStatus` | `trxId` (merchant order/partner reference) |
| One-Time Direct Debit | `latestTransactionStatus` | `originalReferenceNo` (provider ref) or `originalPartnerReferenceNo` |

Always reconcile on the merchant order id / `trxId` as the primary key, not on the VA number or a display label.

## Notification Routing And Response Contract

BI-SNAP notifications use **product-specific standardized callback paths** (confirm exact paths against current docs), for example:

- Direct Debit: `/v1.0/debit/notify`
- QRIS MPM: `/v1.0/qr/qr-mpm-notify`
- Virtual Account: `/v1.0/va/notify`

The notification signature is verified over the **exact request path**: `POST:${requestPath}:${bodyHashHex}:${timestamp}`. Because the path is part of the signed string, a single dispatcher route that rewrites, normalizes, or strips the path before verification will fail signature checks. If you consolidate handling, preserve the literal request path the provider calls in the string-to-sign, and register that exact path in the dashboard.

Return the **BI-SNAP-standard response envelope per product**, not a generic `200 OK`. Shapes vary by product (confirm against docs), for example:

| Product | Success response | Bad-signature response |
| --- | --- | --- |
| Virtual Account | `2002500` with echoed `virtualAccountData` (`partnerServiceId`, `customerNo`, `virtualAccountNo`, `trxId`) | `4012500` Unauthorized |
| QRIS MPM | `2005200` | `4015200` Unauthorized |
| Direct Debit | `2005600` | `4015600` Unauthorized |

Notification handling must:

- read the raw request body for signature verification (do not re-serialize before hashing),
- verify `X-SIGNATURE` before any mutation,
- gate any verification bypass to non-production only — never skip verification in production, including when the public key is missing,
- map the per-product status field through one shared status rule,
- be idempotent and monotonic,
- log only safe business identifiers (order id, provider reference, product, status code),
- return the product-specific success envelope only after accepting or safely recording the callback.

## Status Mapping

Do not copy a status map blindly across products. For each method, load the current status docs and define:

- provider status/code,
- local status,
- terminal/non-terminal behavior,
- allowed regressions,
- ignored stale updates,
- refund/partial refund handling.

Illustrative SNAP-standard status codes (confirm per product against current docs):

| Code | Typical meaning | Suggested local status |
| --- | --- | --- |
| `00` | Success / paid | `paid` |
| `01`, `02`, `03` | Initiated / pending / processing | `pending` |
| `04` | Refund | `refunded` |
| `05`, `06`, `08`, `09`, `99` | Failed / cancelled / expired / void | `cancelled` (map to your failure terminology) |

Treat any unknown code as non-terminal (`pending`) and reconcile via status lookup rather than guessing.

Production-learned rule: do not allow late pending/cancelled provider updates to overwrite paid, fulfilled, shipped, delivered, or refunded local states unless the provider event is a valid refund/chargeback transition.

## Operational Guardrails

- **Access-token caching**: cache the B2B token with an expiry buffer and a single-flight/refresh-race guard. Uncached tokens cause avoidable load and rate pressure.
- **Charge rate-limiting**: apply a per-user fixed-window limit on charge creation (for example, a small number of attempts per minute) and surface a retry-after to the client. This protects the provider integration from accidental loops and abuse.
- **Redaction**: never log private keys, client secret, access tokens, `X-SIGNATURE`, the Midtrans public key, full provider payloads, or customer PII. Log allowlisted business identifiers only.
- **Sandbox-only bypass**: if you allow a missing-public-key verification bypass for local development, gate it strictly to non-production and fail closed in production.

## Troubleshooting Matrix

| Symptom | Likely cause | Agent check |
| --- | --- | --- |
| 401 on access token | Wrong access-token string-to-sign, wrong or invalid private key, or timestamp timezone drift | Verify `clientId\|timestamp` RSA-SHA256, normalized PEM, and Asia/Jakarta ISO-8601 timestamp. |
| 401 on a transaction | Body reformatted after signing, wrong endpoint path in the string-to-sign, or stale access token | Hash the exact sent bytes; match `method:path:token:bodyHash:timestamp`; refresh the token. |
| 401 on a notification | Path rewritten before verification, body re-serialized, or wrong public key | Verify over the literal request path and raw body; confirm the Midtrans notification public key. |
| VA create returns 400 | `partnerServiceId` not exactly 8 characters | Left-pad the numeric service id with spaces to 8 characters in the provider client. |
| Order stuck pending after payment | Wrong per-product status field or reconciliation key, missing idempotency, or unreachable notify URL | Check the per-product status field/reconciliation table, idempotent guard, and notify URL reachability. |
| Notifications rejected or retried by the provider | Returning a generic `200` instead of the product-standard response envelope | Return the product-specific success envelope (and echo `virtualAccountData` for VA). |
| Token churn / rate pressure | Access token not cached, or a refresh race minting many tokens | Cache with an expiry buffer and a single-flight refresh guard. |

## Production Checklist

- Sandbox and production credentials, keys, and base URLs are separated and environment-gated.
- Private key and notification public key are normalized from env and validated at startup.
- Access token is cached with an expiry buffer and refresh-race guard.
- Transactional bodies are signed as the exact sent bytes; no post-signing reformatting.
- Each product's notification URL is registered at its standardized path and is public HTTPS returning the product-standard envelope.
- Notification signature is verified over the literal request path in production with no bypass.
- Per-product status field and reconciliation key are mapped through one shared, idempotent, monotonic rule.
- Charge creation is rate-limited per user with a retry-after.
- Logs redact keys, secrets, tokens, signatures, full payloads, and customer PII.
- Status map is confirmed per product against current docs, including refund/partial-refund handling.
