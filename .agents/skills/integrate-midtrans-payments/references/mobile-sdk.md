# Mobile WebView And SDK Playbook

Use this when the merchant runs an Android, iOS, Flutter, React Native, or other mobile app and wants Midtrans payments. Mobile flows add constraints that web checkout does not have: in-app browser behavior, app-switching to e-wallets, deeplink/universal-link return, and cold-start recovery.

Always refresh current docs from `https://docs.midtrans.com/llms.txt`, especially Snap mobile/WebView, Mobile SDK status, hybrid framework FAQ, GoPay callback URL, and wallet deeplink behavior.

Before building mobile code, complete [merchant-readiness-preflight.md](merchant-readiness-preflight.md). Confirm the merchant has sandbox credentials, active methods, a public backend token endpoint, registered app schemes/universal links, real-device test access, and a clear proof level.

## Contents

- Choose The Mobile Strategy
- Snap WebView / In-App Browser
- Core API Custom Mobile UI
- Native Mobile SDK
- Hybrid Frameworks
- Mobile-Specific Gotchas
- Mobile Production Checklist

## Choose The Mobile Strategy

| Strategy | Use when | Cautions |
| --- | --- | --- |
| **Snap WebView / in-app browser** | New mobile checkout with hosted UI and existing Snap backend | Default path for most Android/iOS/Flutter/React Native apps |
| **Core API custom UI** | Merchant owns native payment UI and accepts higher PCI/payment-method complexity | Build method-specific UI, instructions, redirects, and status recovery yourself |
| **Native Mobile SDK** | Existing native integration, or merchant explicitly accepts current SDK deprecation/support risk | Do not start here blindly; current docs announce Mobile SDK support is being deprecated |

For new mobile work, default to **Snap WebView** unless the merchant needs custom UI. Official hybrid guidance says Midtrans does not provide official Flutter/React Native SDKs; use Snap in WebView or Core API instead.

## Snap WebView / In-App Browser

### Backend

Use the same server-side Snap token flow as web checkout:

- Merchant backend calls `POST {host}/snap/v1/transactions` with Basic Auth using the server key.
- Backend returns the exact `redirect_url` or `token` to the app.
- Mobile app never receives the server key and never builds provider-signed payloads.
- Persist payment attempt state before returning the URL so refresh, app backgrounding, or cold start can recover.

### Mobile app

Open the exact Snap `redirect_url` returned by Midtrans. Do not rewrite, shorten, encode, or decorate it unless current docs explicitly require a documented query option such as GoPay display mode.

Required WebView/in-app-browser behavior:

- Enable JavaScript; Snap renders through JS.
- Enable cookies and session storage.
- Allow navigation to Midtrans hosts and external wallet/universal/deep links.
- Intercept finish, unfinish, and error redirect URLs to dismiss the WebView.
- Treat every redirect or SDK callback as a UX hint only; call the backend status endpoint or wait for the webhook before fulfillment.
- Persist the local order/payment id outside component memory so app backgrounding and process death can resume.

### Wallet app-switch return

GoPay/ShopeePay-style methods can switch from the merchant app to a wallet app and back. Current docs warn that default WebView behavior can block app-based redirect URLs.

Implement this as an OS-level app-link/deeplink concern, not only as an in-WebView callback:

1. Register and test iOS URL schemes/universal links and Android intent filters/app links.
2. On iOS, add wallet schemes required by current docs to `LSApplicationQueriesSchemes` when the app must open external wallet links.
3. Configure WebView navigation delegates to open supported external schemes with the OS instead of failing with unknown URL scheme errors.
4. If using GoPay `callback_url`, set it only after the merchant app deeplink is registered and tested. Deeplink URLs are allowed, but QR-scan payments may return through a different path.
5. On return, dismiss or restore the payment surface and verify status through the backend.

For Snap redirect mode, current docs also expose GoPay display options such as forcing deeplink or QR behavior. Use them only when the merchant has tested the resulting app-switch or QR path on real devices.

## Core API Custom Mobile UI

Choose Core API when the merchant wants native screens for each method instead of a hosted payment page.

The app may render QR, VA, OTC instructions, card forms, or wallet redirect buttons, but the backend still owns provider calls, signatures, keys, and status mapping. For cards, tokenize PAN/CVV in Midtrans-approved frontend boundaries; do not send raw card details to the merchant backend.

Core API mobile work must also implement:

- method-specific instruction storage and recovery,
- expiry and retry behavior,
- external wallet deeplink handling,
- backend status lookup after app return,
- idempotent webhook reconciliation.

## Native Mobile SDK

Treat native Mobile SDK as a legacy or existing-integration path unless current docs and Midtrans support confirm it is appropriate for the merchant. The docs home page currently announces that Mobile SDK support is being deprecated and new feature development/integration support are no longer available.

If the merchant already uses the native SDK:

- Confirm the current Android/iOS installation path from docs or Midtrans support; do not hardcode outdated Gradle coordinates, CocoaPods, or Swift Package assumptions.
- The SDK must call the merchant backend token endpoint; it must not call Midtrans with the server key from the app.
- SDK payment completion callbacks are UX hints only. Fulfillment still depends on webhook or backend status verification.
- Include an exit plan to Snap WebView or Core API if platform updates, app-store requirements, or deprecated SDK behavior block release.

## Hybrid Frameworks

Flutter, React Native, Cordova, and similar stacks should start with Snap WebView/in-app browser or Core API. Community packages can be used only after checking maintenance status, native dependency freshness, and whether they preserve the exact Snap redirect URL and app-switch behavior.

## Mobile-Specific Gotchas

- **SDK deprecation missed**: new native SDK work can become unsupported. Prefer Snap WebView/Core API unless the merchant accepts the risk.
- **Redirect URL modified**: changing the Snap URL can break wallet app-switch. Preserve the exact URL returned by Midtrans.
- **Cookies or JavaScript blocked**: Snap can silently fail or restart sessions.
- **Unknown URL scheme**: Android WebView may show `ERR_UNKNOWN_URL_SCHEME`; iOS may block wallet schemes unless app queries/deeplink handling are configured.
- **QR vs deeplink mismatch**: QR-scan payments may not return through `callback_url`; provide polling or push fallback.
- **Trusting mobile callbacks**: redirects, WebView close events, and SDK callbacks are not payment proof.
- **Cold start**: wallet return can reopen the app after process death. Recover from durable order/payment state.

## Mobile Production Checklist

- Merchant backend token/status endpoints work from real devices and production-like networks.
- Sandbox and production keys are separated; the app cannot accidentally point a debug build at production.
- Real Android and iOS device tests cover card/3DS, GoPay deeplink, QR scan, close/unfinish/error, background, killed-app return, flaky network, and missed-return polling.
- WebView/in-app-browser config allows JavaScript, cookies, Midtrans hosts, and external wallet links.
- App links/deeplinks are registered, verified, and routed to backend status lookup.
- Customer fulfillment happens only after webhook or backend status verification.
- Logs redact tokens, redirect URLs with sensitive query parameters, signatures, auth headers, and customer PII.
