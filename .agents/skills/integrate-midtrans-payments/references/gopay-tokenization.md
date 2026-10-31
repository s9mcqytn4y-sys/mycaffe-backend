# GoPay Tokenization And GoPayLater Playbook

Use this with BI-SNAP when the merchant asks for GoPay account linking, linked GoPay wallet payments, GoPayLater, account unlinking, binding inquiry, `Authorization-Customer`, or payment option token bugs.

Always refresh current docs from `https://docs.midtrans.com/llms.txt`, especially Account Linking API, Get Auth Code API, Binding API, Binding Inquiry API, Unbind API, Direct Debit API GoPay Tokenization, account-linking/unlinking notification, and GoPay tokenization sandbox testing. For concrete tokenization sandbox checks and credential-safe evidence, use [sandbox-interaction-helper.md](sandbox-interaction-helper.md).

## Separate The Flows

Do not collapse these into one request shape:

- **One-time GoPay/ShopeePay/DANA Direct Debit**: customer pays through a redirect/deeplink without stored customer authorization token.
- **Account linking**: customer authorizes the merchant to link a GoPay account.
- **Linked GoPay wallet payment**: merchant charges using stored customer authorization and current wallet payment option token.
- **GoPayLater payment**: merchant charges using current `PAY_LATER` payment option token, only when activated and available for the linked account.
- **Unlinking**: merchant or provider removes the authorization and local token state must be cleared/reconciled.

## Token-First State Model

Key integration lesson: the durable credential is the customer authorization token, not a legacy account id.

Persist server-side:

- user/customer id,
- linking state/CSRF state,
- linking reference,
- customer authorization token,
- token expiry if provided,
- account/payment options from Binding Inquiry,
- last inquiry timestamp,
- linked/unlinked status and status reason,
- non-sensitive provider metadata for audit.

Never return customer authorization tokens, auth codes, payment option tokens, seamless signatures, or provider secrets to the browser.

## Account Linking Flow

Expected shape:

1. Get B2B access token.
2. Start Get Auth Code request on the correct app/merchant host from current docs.
3. Include merchant handle, redirect URL, language, state/CSRF value, and seamless data/signature when required.
4. Redirect user to GoPay authorization.
5. On return, validate state before binding.
6. Call Binding API with auth code.
7. Store returned customer authorization token and safe account metadata.
8. Call Binding Inquiry to confirm linked state and load current payment options.

**Host gotcha**: `get-auth-code` uses the `merchants-app` host (`merchants-app.midtrans.com` / `merchants-app.sbx.midtrans.com`), while binding, inquiry, unbind, and Direct Debit use the BI-SNAP merchant host (`merchants.midtrans.com` / `merchants.sbx.midtrans.com`). Verify current docs and merchant configuration before coding.

**Seamless data signing**: when current docs require a `seamlessData` field, the value must be **URL-encoded with `encodeURIComponent(...)` before** RSA-SHA256 signing with the merchant private key. The query-string layer then handles outer encoding. Signing the raw, un-encoded seamlessData produces signatures the provider silently rejects. This is one of the most expensive sandbox debugging traps in GoPay linking.

**Merchant handle vs merchant id**: many merchants need two distinct identifiers:

- A **linking merchant handle** used in `get-auth-code` and Binding (often a UUID-like value provided by Midtrans during onboarding).
- A **charging merchant id** used in Direct Debit charges and QRIS (the standard `merchantId`).

Use separate env vars (e.g., `BISNAP_GOPAY_MERCHANT_HANDLE` for linking, `BISNAP_GOPAY_MERCHANT_ID` for charging). Conflating them is a common source of "linking works, charging fails" or vice versa.

## Binding Inquiry Before Payment

Call Binding Inquiry before tokenized wallet or PayLater payment because payment option tokens can change. Binding Inquiry can also return a **rotated `customer_authorization_token`** in `additionalInfo.accessToken`; treat the inquiry response as the source of truth and persist the latest token immediately. Refusing to refresh leads to "valid yesterday, rejected today" failures.

For linked wallet:

- require an active `GOPAY_WALLET` option,
- use `Authorization-Customer`,
- use the current account/payment option token required by docs,
- disable or route to linking when unavailable.

For GoPayLater:

- require merchant activation,
- require linked account,
- require active `PAY_LATER` option from inquiry,
- keep hidden or disabled until sandbox smoke is verified,
- include risk/additional data required by Midtrans before production.

## Unlinking And Notifications

Unbind through the provider API using the current customer authorization rules. On success, clear or mark local token state as unlinked.

Account-linking/unlinking notifications should:

- verify signature/authenticity,
- store or log a safe receipt,
- reconcile local linked/unlinked status,
- fall back to Binding Inquiry when notifications are missing or ambiguous.

## Debugging Symptoms

| Symptom | Likely cause | Direction |
| --- | --- | --- |
| Linked payment says invalid customer token | Wrong/expired customer authorization token, wrong environment, or token not stored from binding | Re-run inquiry, verify env, inspect token persistence. |
| PayLater visible but charge fails | Merchant/account not activated or no active `PAY_LATER` option | Gate UI by activation plus Binding Inquiry result. |
| Wallet worked before but now fails | Payment option token changed | Refresh Binding Inquiry immediately before payment. |
| Relinking same phone fails | Merchant account may need relinking support or local state is inconsistent | Check current docs and Midtrans activation/support notes. |
| Logs leak auth code or option token | Redaction missing | Add allowlist logging and redact token-like fields. |
