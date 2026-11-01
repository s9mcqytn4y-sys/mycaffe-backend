---
trigger: model_decision
description: "Aktif untuk checkout, QRIS, Midtrans, webhook, refund, payment status, pricing, tax, settlement, dan split bill."
---

# Payment and QRIS security rules

- Recalculate price and validate stock/modifiers on the server immediately before creating the payable transaction.
- Store money as integers in IDR. Reject invalid, negative, overflowed, or mismatched amounts.
- Separate `payment_status` (for example pending, paid, failed, expired, refunded) from `order_status` (for example queued, preparing, ready, completed, cancelled). Use explicit transition rules.
- Never mark a payment paid from the frontend redirect, screenshot, customer claim, or a request parameter. Verify signed provider notifications using the current official Midtrans documentation and reconcile the notification against the expected order, amount, and transaction.
- Webhook handling must be idempotent. Replayed or out-of-order notifications must not double-count sales, create duplicate kitchen tickets, or reverse a valid final state without an explicit provider-supported transition verified via Midtrans Get Status (`CheckTransaction`).
- Use a 2-phase database transaction for gateway payment initiation (`PaymentService.InitiatePayment`): persist the `pending` payment attempt in DB Tx 1, call Midtrans Core API outside DB locks using `payments.id.String()` (36-char UUID) as both `provider_order_id` and Midtrans `Idempotency-Key`, reconcile ambiguous timeouts with `CheckTransaction`, and persist gateway details in DB Tx 2.
- If a late settlement arrives after an unpaid order's stock reservation was released (`stock_status = 'released'`) and stock is now exhausted, use `SAVEPOINT payment_stock_commit` to roll back only the stock deduction while still committing the `paid` financial record and emitting `payment.late_settlement_stock_exhausted`.
- Do not log payment secrets, authorization headers, full tokens, or unnecessary personal information.
- Model static merchant QRIS and dynamic Midtrans QRIS as distinct payment modes. Static QRIS generally cannot be automatically matched to an exact order without a verified reconciliation source.
- Make MDR, service charge, tax, takeaway fee, and settlement assumptions configurable and traceable to a store/provider setting. Never hardcode a universal 0.7% MDR or 10% tax as a legal/payment fact.
- Split bill requires a parent order plus explicit allocations/payments and reconciliation rules. Do not implement it as a cosmetic division of the displayed total.
- Use Midtrans Sandbox for development and staging. Real-money operations require explicit owner approval and production credentials must remain outside the repository.
