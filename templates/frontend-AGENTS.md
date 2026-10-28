# MyCaffe Core POS — Frontend Repository Instructions

This file is a template for the separate Next.js frontend repository. Copy it as `AGENTS.md` into that repository only after checking its current files.

## Stack and boundaries
- Next.js App Router, TypeScript, Tailwind CSS, PWA only if confirmed in the actual package setup.
- Mobile-first customer ordering; tablet/desktop-first POS/KDS; clear, large, touch-friendly operational controls.
- The Go backend is the source of truth for pricing, taxes/fees, stock, authorization, order state, and payment state.
- Never trust frontend totals, store IDs, table numbers, payment return URLs, or local storage as proof of authorization/payment.
- Use Bahasa Indonesia for user-facing copy. Keep code identifiers in English.
- Use the repository's existing package manager and lockfile. Do not change package managers without approval.

## UI and state
- Keep cart line identity stable so the same product with different modifiers/notes remains distinct.
- Make loading, empty, sold-out, network-failure, expired-payment, and retry states explicit.
- Avoid account creation for guest ordering unless a store requirement explicitly demands it.
- Use `localStorage` only for non-sensitive recovery hints/cart state; do not store secrets or authoritative payment status.
- Accessibility: semantic controls, keyboard support, contrast, focus visibility, and screen-reader labels.
- Measure performance on realistic mobile hardware/network; do not promise a universal sub-two-second load.

## API and payment
- Consume documented Go API contracts. Do not implement a second business-logic layer in Server Actions/API routes.
- Never mark a payment successful based on a redirect or client callback. Refresh/poll/subscribe to backend-confirmed state.
- Handle idempotent submission, network retry, duplicate clicks, and stale stock responses.
- Treat Realtime delivery as eventually consistent and implement reconnect/re-fetch behavior.

## Verification
- Inspect current scripts before running checks. Run lint, typecheck, tests, and production build where configured.
- Use Playwright for critical journeys: guest ordering, modifier combinations, checkout errors, payment status, cart recovery, cashier, KDS, and admin authorization UI.
- Report actual results and known limitations.
