# Merchant Decision Tree

Use this first when the merchant's target product or payment scope is unclear. Do not force any one merchant's prior provider split onto the next merchant; use observed splits as learned patterns, not universal requirements.

Refresh current Midtrans product/API details from `https://docs.midtrans.com/llms.txt` before final product routing.

Complete [merchant-readiness-preflight.md](merchant-readiness-preflight.md) before final product routing. Product fit depends on merchant account state, active methods, dashboard access, and expected proof level, not only on API shape.

## Terminology And Aliases

Normalize these terms before routing; mismatched vocabulary is a common source of agent and merchant confusion, and some of it is not spelled out in the public docs.

- **Snap** is also called **Snap Checkout**; both names refer to the same Midtrans-hosted checkout product.
- **Legacy Core API** means the non BI-SNAP Core API. When this skill says "Core API" without qualification, treat it as the legacy (non BI-SNAP) Core API unless the surrounding context is explicitly BI-SNAP.
- A merchant's business-level **Payment Method** maps to different API parameters per product: `enabled_payments` on Snap and `payment_type` on Core API. Translate the merchant's chosen Payment Method into the correct parameter for whichever product you route to.
- Payment Method **"Card"** covers both **Credit Card and Debit Card**; a single card flow (Snap or Core API `credit_card`) typically accepts payment from customers using either.

## "Callback" Is Overloaded

"Callback" means several unrelated things in Midtrans depending on context. Identify which one the merchant means before wiring anything, and prefer the precise term in code and docs.

- **Transaction HTTP Notification** — the server-to-server transaction status notification. Prefer the term **HTTP Notification**; it is also widely known as a **Webhook**. This is the only trustworthy fulfillment signal.
- **GoPay tokenization account-linking HTTP Notification** — a separate server-to-server notification for account-linking status, distinct from the transaction notification above.
- **Snap Checkout frontend JS callback** — `onSuccess`/`onPending`/`onError`/`onClose` fired by the Snap iframe popup. UX hint only, never fulfillment proof.
- **Snap Checkout finish redirect URL** — in Snap redirect-URL mode, the browser returns to the merchant's finish/unfinish/error URL. UX only.
- **Core API card frontend JS callback** — fired by the 3DS iframe popup during card payment. UX hint only.
- **Payment-method finish redirect URL (`callback_url`)** — the API parameter literally named `callback_url`, e.g. where the customer returns after completing a GoPay payment in the GoPay app. UX only.
- **GoPay tokenization linking finish redirect URL** — the browser return URL after account linking completes. UX only.

## First Questions

Ask or infer:

- Does the merchant already have a Midtrans account/MID and sandbox dashboard access?
- Are sandbox credentials available through env/secrets, and are production credentials intentionally out of test paths?
- Does the merchant accept a Midtrans-hosted payment UI?
- Does the merchant need an app-owned checkout UI for each payment method?
- Which methods are required now: card, OTC, VA, QRIS, GoPay, ShopeePay, DANA, GoPay tokenization, GoPayLater, recurring, refunds?
- Is this web, mobile web, native app, POS/IoT, MiniApp, marketplace, or no-code collection?
- Does the merchant already have Midtrans activation for the requested methods?
- Does the app already have orders, users, payment state, webhooks, logs, and env/secret management?
- What customer flow is being implemented: cart checkout, locked invoice payment, renewal, deposit, marketplace order, or manual collection?
- What proof is expected in this session: design review, local deterministic checks, sandbox provider smoke, or production penny test?

## Route The Request

| Need | Recommend | Why |
| --- | --- | --- |
| Fastest checkout, Midtrans-Hosted UI is fine | Snap | Midtrans owns payment UI, PCI-sensitive card UI, and method-specific screens. |
| Own checkout selects method, but hosted method screen is acceptable | Snap with `enabled_payments` | Merchant keeps a unified checkout while Snap handles the selected method flow. |
| No app or invoice/chat collection | Payment Link | Avoid building checkout code when a shareable link is enough. |
| Fully custom QRIS, VA, e-wallet, or SNAP-standard payment APIs | BI-SNAP | Merchant owns UI and must handle access tokens, signatures, callbacks, and status reconciliation. |
| GoPay tokenized wallet or GoPayLater | BI-SNAP tokenization | Requires account linking, Binding Inquiry, stored customer authorization token, and payment option tokens. |
| Merchant-hosted custom Card Payment's UI | Core API | Requires stricter card, PCI, and 3DS decisions. Prefer Snap unless custom card UX is a real requirement. |
| GoPay MiniApp container | MiniApp docs plus BI-SNAP payment docs | Different runtime and UX constraints from normal web checkout. |

## Hybrid Is Often Correct

A merchant can combine products if the ownership is explicit. One real merchant uses Snap for card/OTC and BI-SNAP for QRIS, VA, GoPay, and GoPayLater. Another merchant might use Snap for every method, or BI-SNAP only for QRIS and VA. The skill should help the agent choose and document a product owner per method.

## Decision Output

Before implementing, state:

- selected product per payment method,
- why each product fits the merchant need,
- activation/dashboard assumptions,
- account/sandbox readiness and any missing merchant answers,
- callback URLs required,
- server-side secrets required,
- local state needed to resume pending payments,
- tests and sandbox smoke needed before go-live.
