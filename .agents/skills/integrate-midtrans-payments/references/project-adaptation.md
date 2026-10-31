# Project Adaptation

Use this checklist before proposing or changing a Midtrans integration.

Refresh current Midtrans product/API details from `https://docs.midtrans.com/llms.txt` after mapping the project and before choosing product-specific APIs.

Start with [merchant-readiness-preflight.md](merchant-readiness-preflight.md). Project inspection must answer both code-shape questions and merchant-readiness questions: account status, sandbox setup, active methods, dashboard URLs, expected customer flows, and proof level.

## Merchant Readiness Scan

Look for existing answers before asking the user:

- `.env.example`, typed settings, secret-manager resources, Terraform variables, deployment env files.
- Payment runbooks, go-live checklists, product specs, billing/invoice docs, support notes, and existing issue comments.
- Existing dashboard/callback URL documentation and redirect URL conventions.
- Feature flags that already gate provider availability or environment selection.
- Tests or fixtures proving sandbox/local behavior.

If the repo has code for Midtrans but no source of truth for account/MID, sandbox credentials, active methods, or expected customer flow, report that as a blocker before adding more provider code.

## Repository Scan

- Read agent/project instructions first: `AGENTS.md`, `CLAUDE.md`, `.cursor/rules/*`, `.github/copilot-instructions.md`, or equivalent.
- Find payment docs and decisions: `docs/**`, ADRs, runbooks, OpenSpec/Superpowers plans, issue notes, postmortems.
- Find checkout entry points, payment method picker, order creation route/controller, payment creation route/controller, status route/controller, webhook routes, provider clients, persistence layer, and tests.
- Find environment/deployment wiring: `.env.example`, typed env config, container config, Terraform/Pulumi/CloudFormation, secret manager bindings, CI drift tests.
- Identify the runtime stack: framework, backend boundaries, database, session/auth provider, queue/job system, logger, deployment platform.

## Architecture Mapping

Build a short map like this before edits:

| Concern | Existing location | Rule |
| --- | --- | --- |
| Checkout UI | components/pages/forms | UI owns state and display only |
| HTTP/API boundary | routes/controllers | Parse, authorize, validate, call domain |
| Domain workflow | services/use-cases | Provider routing and status rules |
| Persistence | repositories/models | SQL/ORM access only |
| Provider transport | midtrans/bisnap clients | Headers, signing, SDK/fetch, payloads |
| Ops | env/deploy/CI | Secrets, callbacks, logs, smoke tests |

If no clear boundary exists, introduce the smallest boundary needed for payment safety. Do not bury provider calls in browser UI or database access in UI components.

## Questions To Answer From Code

- What merchant readiness facts are confirmed, inferred, or missing?
- Does the merchant already have a Midtrans account/MID and sandbox dashboard access?
- Which environment is being implemented now: local deterministic only, sandbox, staging, or production?
- Which exact customer flow is expected: checkout, invoice payment, subscription, marketplace, payment link, tokenized wallet, or another flow?
- Which payment methods are active in sandbox and intended for production?
- What public callback and browser redirect URLs must be configured in the dashboard?
- What is the merchant order identifier, and does it stay stable across payment retries?
- Where is the canonical order/payment state stored?
- Is the order transitioned to a `creating_payment` (or equivalent) state **before** the provider call, so a provider failure leaves the order in an identifiable, retryable state instead of an ambiguous one?
- Which statuses are terminal, and which provider statuses may be ignored after terminal state?
- Can a user refresh or return later and still see payment instructions?
- Are callbacks authenticated, idempotent, and safe to replay?
- Is there a server-side status polling path for reconciliation?
- Are provider secrets available only on the server?
- Are sandbox and production keys/envs separated?
- Are payment methods hidden or disabled when activation/feature flags are missing?

## Generalizable Lessons From Real Integrations

- Keep provider split explicit. One real merchant uses Snap for card/OTC and BI-SNAP for QRIS, VA, GoPay, and GoPayLater. Other merchants may choose differently, but every method must have one owner.
- Keep checkout state-driven. Submitting payment by scraping DOM fields makes recovery and verification harder.
- Mark the order `creating_payment` (or equivalent) **before** the provider HTTP call. If the provider fails, the order is in a clearly identifiable, retryable state — not an ambiguous "we may or may not have created a payment" state.
- Store provider-specific resume data in order/payment state. QR, VA, redirects, references, expiry, and status payloads are not transient UI details.
- Add environment variables in all places at once: example env, typed env validation, production secret binding, deployment config, and drift tests.
- Log payment creation and callbacks with business identifiers such as order id and provider reference. Avoid dumping unrestricted payloads.
- Use feature flags and merchant activation gates for GoPay tokenization and GoPayLater. Hide or explain unavailable methods instead of creating doomed requests.
- A technically sound Snap invoice integration can still be premature if it never established whether the merchant has a Midtrans account, sandbox keys, active methods, and agreed invoice-payment flow. Treat those as design inputs, not post-implementation cleanup.
