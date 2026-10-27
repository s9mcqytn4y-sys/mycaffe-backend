# Architecture decisions to settle before implementation

This document records decision gates, not assumed facts. Do not silently choose an option when it changes operational risk or recurring cost.

## 1. Production database vs Realtime

The current proposal says Development/Staging use Supabase and Production uses native PostgreSQL on a Hostinger VPS. The PRDs also rely on Supabase Realtime for the KDS, customer order tracker, and TV queue. Native PostgreSQL alone does not provide Supabase Realtime.

Choose one before implementing cross-device realtime:

- **Option A — Supabase in all environments for the MVP (recommended for a small team):** one database product and Realtime model across dev/staging/production; reduce VPS operations burden. Review ongoing usage, data location, and provider dependency.
- **Option B — Native PostgreSQL in production:** implement and operate a Go WebSocket/SSE event layer or another explicitly chosen realtime mechanism. Add TLS, authorization, reconnect/replay behavior, event delivery guarantees, backups, monitoring, restore drills, upgrades, and incident response.

Do not assume that moving a PostgreSQL database to a VPS preserves Supabase Realtime behavior. Record the final decision in an ADR.

## 2. Static QRIS vs dynamic QRIS

These are different product modes:
- Static merchant QRIS: simple to display, but automatic order-to-payment matching must not be promised unless a verified provider/reconciliation integration supports it.
- Dynamic gateway QRIS: create an order-linked transaction and confirm state through verified provider notifications.

Store configuration may allow either mode, but the backend must model their verification and reconciliation differences. Test whether target banking/e-wallet apps support importing a saved QR image; provide a second-device or cashier-assisted fallback.

## 3. Payment state vs preparation state

Use separate state machines. Suggested payment states: `pending`, `paid`, `failed`, `expired`, `refunded` (final set must follow gateway behavior). Suggested order states: `placed`, `queued`, `preparing`, `ready`, `completed`, `cancelled`. Define legal transitions and how late/duplicate notifications are handled before coding.

## 4. Multi-tenant foundation

Decide the canonical store/tenant identity and authorization model before creating business tables. All store-owned resources—menu, modifier groups, orders, tables, staff, settings, expenses, audit logs, reports—must be scoped consistently. Human-readable order and queue numbers are display IDs, not database primary keys.

## 5. Taxes, service charges, MDR, HPP, and reporting

Do not hardcode a universal PB1/tax percentage, QRIS MDR, service charge, settlement time, or fee policy. Configure store/provider values, preserve the applied values on each order, and distinguish gross sales, net sales, gross profit estimate, and net profit. A credible net-profit report needs complete expense and cost data; otherwise label it an estimate.

## 6. Staff PIN and offline claims

A 4-digit PIN appears in one PRD and a 6-digit PIN in another. Resolve the policy; default proposal is six digits plus rate limiting, lockout/backoff, salted strong hashing, role checks, and short-lived sessions. A local JWT does not make remote database sync or online payment work offline. Define the offline scope separately and never show a payment as paid while verification is unavailable.

## 7. Deployment and backups

Keep backend and frontend repositories/deployments separate if that remains the selected strategy. Pin third-party GitHub Actions to reviewed versions/SHAs where practical. Do not rely on a GitHub Actions cron as the sole production database backup system; define retention, encryption, restore testing, and monitoring.

## 8. Current workspace boundary

The current workspace is `myCaffe-backend`. Do not create frontend files here unless the owner explicitly asks for a monorepo. Frontend-specific rules are provided as a template under `templates/`.
