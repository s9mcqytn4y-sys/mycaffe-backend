---
name: midtrans-qris-integration
description: "Implements or reviews Midtrans QRIS checkout, payment notifications, reconciliation, expiry, and refund handling."
---

# Midtrans QRIS integration workflow

1. Confirm the intended mode: static store QRIS/manual reconciliation or dynamic Midtrans QRIS linked to an order. Do not conflate them.
2. Read the current official Midtrans documentation for the selected product/API and environment. Do not rely on remembered endpoint names or assumed fee/settlement terms.
3. Define payment states, order states, allowed transitions, expiry behavior, retry behavior, idempotency keys, and reconciliation requirements before coding.
4. Calculate totals on the server from catalog snapshots and store-configured rules; do not trust frontend totals.
5. Create a pending payment/order record safely, then call the gateway with a stable unique transaction identity. Avoid duplicate charges on retry.
6. Verify provider notifications according to the current official signature/authentication specification. Reconcile transaction identity, expected amount, currency, and final provider status.
7. Make webhook processing idempotent and safe for repeated/out-of-order events. Only a confirmed valid payment may create a kitchen queue event.
8. Keep sandbox and production credentials separate. Automated tests must use mocks/fixtures or sandbox, never real-money calls.
9. Add tests for invalid signatures, amount mismatch, duplicate notification, timeout, expired payment, cancelled/denied status, and success.
10. Document what the browser can and cannot guarantee when the customer uses one phone for both the menu and mobile banking.
