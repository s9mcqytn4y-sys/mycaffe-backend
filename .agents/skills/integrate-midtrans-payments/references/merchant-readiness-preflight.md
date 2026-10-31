# Merchant Readiness Preflight

Use this before product routing, implementation, sandbox proof, or go-live advice. Refresh current Midtrans account, payment-method, dashboard, and product docs from `https://docs.midtrans.com/llms.txt`.

The goal is to prevent an agent from building a technically clean integration for a merchant state that does not exist yet: no Midtrans account, no sandbox keys, inactive methods, missing callback URLs, unclear payment flow, or no agreement on what proof counts.

## Contents

- Required Output Before Code
- Account And Dashboard
- Credentials And Environment
- Flow And Product Scope
- Activation And Method Gating
- Callback And Redirect Readiness
- Proof Level
- Example: Invoice Snap Flow

## Required Output Before Code

Before editing code, produce a short readiness summary:

```text
Merchant readiness:
- Account/MID:
- Sandbox access:
- Sandbox credentials:
- Target environment:
- Requested customer flows:
- Payment methods and activation:
- Callback and redirect URLs:
- Expected proof:
- Operational owner:
- Blockers before provider calls:
```

Use `confirmed`, `inferred from <file>`, or `missing` for each line. If any missing item blocks safe implementation or sandbox calls, ask for it directly and do not invent it.

## Account And Dashboard

Confirm or infer:

- Does the merchant already have a Midtrans account or MID for this business?
- Is this an individual, business entity, partner, marketplace, or multi-outlet setup?
- Who has access to the Midtrans Administration Portal for sandbox and production?
- Are sandbox and production accounts/environments separated?
- Which team owns payment-method activation, dashboard URLs, refunds, settlement reports, and support escalation?

If no Midtrans account exists yet, do not implement production cutover tasks. Provide a registration/onboarding checklist and limit code work to local scaffolding that does not call Midtrans.

## Credentials And Environment

Confirm or infer:

- Sandbox server key/client key exist and are loaded from env or secret manager, not chat.
- Production credentials are absent from local tests unless the user explicitly approved a live runbook.
- `is_production` or equivalent cannot accidentally point sandbox keys at production URLs or production keys at sandbox URLs.
- Base URL overrides, callback URLs, and redirect URLs are environment-specific.
- CI, deployment config, secret manager bindings, and drift tests cover every required variable.

For Snap, server key belongs backend-only; client key is only for Snap JS. For BI-SNAP, private keys, client secrets, access tokens, and customer authorization tokens belong server-side only.

## Flow And Product Scope

Write down the exact customer and operations flow:

- Customer trigger: cart checkout, invoice collection, subscription renewal, paywall, deposit, marketplace order, or manual payment link.
- Display path: Snap redirect, Snap popup, Snap embed, app-owned QR/VA UI, mobile deeplink, or no-code Payment Link.
- Required methods now versus later: card, VA, QRIS, GoPay one-time, GoPay tokenization, GoPayLater, OTC, ShopeePay, DANA, refunds.
- Retry behavior: when to reuse an active attempt, when to create a new provider order id, when to expire/cancel.
- Recovery behavior: how the user returns after refresh, browser callback, closed popup, failed provider call, missing webhook, or pending async payment.
- Fulfillment rule: which backend state transition releases goods/services or marks an invoice paid.

If the flow is vague, ask for a concrete user story before coding. Example: "Facility admin pays a locked monthly invoice using Snap redirect, then the invoice becomes paid only after a verified notification or backend status sync."

## Activation And Method Gating

For every requested method, capture:

```text
Method:
Target product:
Sandbox active:
Production active:
Dashboard/config owner:
Fallback if inactive:
Verification path:
```

Do not show a payment method as usable merely because code supports it. Hide, disable, or label unavailable methods until merchant activation and dashboard configuration are confirmed.

## Callback And Redirect Readiness

Confirm or infer:

- Payment Notification URL is public HTTPS, not localhost, not behind auth/VPN, and does not redirect.
- Finish, unfinish, and error redirect URLs point to UX pages only; they are not fulfillment proof.
- Local webhook tests use a tunnel or deterministic replay; they are not equivalent to dashboard-delivered notifications.
- The app has a status polling or sync route for missed callbacks.
- Webhook handlers can be replayed safely and return 2xx only after durable acceptance.

## Proof Level

Agree on what evidence the merchant expects:

| Proof level | Acceptable evidence | Do not claim |
| --- | --- | --- |
| Local design review | File inspection, plan, risk list | Working integration |
| Local deterministic proof | Unit tests, signature fixtures, payload builders, webhook replay | Sandbox end-to-end |
| Sandbox provider proof | Sandbox token/charge, dashboard callback, status polling, logs | Production readiness |
| Production penny test | Merchant-approved low-value live run with monitoring | General testing without approval |

When credentials, account access, or active methods are missing, continue only with the lower proof levels and state exactly what remains unproven.

## Example: Invoice Snap Flow

For a B2B invoice-payment request, do not start with code. First capture:

- merchant has a Midtrans account and sandbox dashboard access,
- Snap is the intended product because hosted payment UI is acceptable,
- invoices become payable only after they are locked and have integer IDR totals,
- sandbox server key is available through backend env/secret manager,
- dashboard notification URL will be `https://<api-domain>/.../midtrans/.../notifications`,
- finish/unfinish/error URLs return users to the billing page,
- proof target is local deterministic tests first, then a sandbox Snap redirect payment.

Only then implement the provider client, payment-attempt model, route, webhook, frontend action, and runbook.
