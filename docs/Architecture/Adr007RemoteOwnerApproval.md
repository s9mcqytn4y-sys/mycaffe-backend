# ADR-007: Remote Owner Approval from the Owner's Phone

- **Status**: Accepted (owner, 2026-11-04). **Implemented & Tested** in v0.7 PR 3 (migration 000024, `TestIntegrationRemoteApproval`).
- **Deciders**: Product owner, backend
- **Amends**: ADR-003. Adds one permission (`approval.decide`, owner only) and one WebSocket channel
  (`owner`). Roles do not change.
- **Related**: decisions DM7 and DM8 in [DecisionRegister.md](DecisionRegister.md), O1 (void/refund
  threshold), RF1-e (refund window), HF5 (shop-wide owner PIN guess limit), and audit ticket P1-04.
  ADR-006 is reserved for the frozen stock ledger.

## 1. Context

Four cashier actions need an owner PIN today, and that PIN is typed on the cashier tablet:

| Action | Owner PIN when | Code |
|--------|----------------|------|
| Void | Order total is above `void_refund_owner_threshold` | `authorizeAmount`, `VoidOrderInput.owner_pin` |
| Refund | Amount is above the threshold, or the refund is past the cashier window (RF1-e); bank-transfer refunds stay owner-only | `authorizeAmount`, `RefundPaymentInput.owner_pin` |
| Stock override | Always | `StockOverrideInput.owner_pin` |
| Shift close | Cash variance is above `cash_variance_tolerance` | `CloseShiftInput.owner_pin` |

The owner has to be at the counter, or has to tell the PIN to a cashier. DM7 asks for approval from the
owner's phone before the demo. DM8 sets the confirmation to the owner's own PIN, entered on the phone.

## 2. Decisions

| ID | Decision |
|----|----------|
| RA1 | **One request per action.** A cashier creates an `approval_request` for exactly one action and one reference: void (`order_id`), refund (`payment_id`), stock override (`product_id` + `new_quantity`), or shift variance (`shift_id` + `counted_cash`). The server computes the amount shown to the owner from the reference. The cashier never supplies an amount. |
| RA2 | **The owner decides with their own PIN** on their own signed-in session (`approval.decide`). The PIN goes through `verifyStaff` on the owner and through the same HF5 counters (`OWNER_PIN_FAILED`, per-actor and shop-wide limits). A wrong PIN answers `401 INVALID_OWNER_PIN`. The owner can also reject, with an optional note. |
| RA3 | **Single use, bound, short-lived.** The owner must decide within **2 minutes** of the request. After approval, the requesting cashier must run the action within **2 minutes**. The action consumes the approval in its own transaction (`SELECT … FOR UPDATE`, status `approved` → `consumed`). It succeeds only if all of these match: the requester, the action, the reference, and the amount recomputed at that moment. Otherwise it fails with `409 APPROVAL_INVALID`, and nothing changes. |
| RA4 | **`approval_id` replaces `owner_pin`, nothing else.** Void, refund, stock override and shift close accept `approval_id` wherever they accept `owner_pin` today. The cashier's own PIN, the reason and every other rule stay the same. The approver's id goes into the same `owner_override` / `approved_by` fields and audit metadata as today, plus `approval_id`. |
| RA5 | **Delivery: WebSocket, then REST.** A new request emits an outbox event `approval.requested` on a new `owner` channel. The ticket grants that channel to staff with `approval.decide`. A decision emits `approval.decided` on the `pos` channel. Both payloads carry only ids and status. Clients read the details over REST: the owner's pending list, and the cashier polling one request. Web Push to a closed app is out of scope for v0.7, because it needs VAPID keys, a new dependency and a stable domain. The owner keeps the PWA open, and the tablet `owner_pin` stays as the fallback. |
| RA6 | **Guard rails.** The requester needs the permission of the underlying action. Owners do not create requests; they use their PIN directly as today. The approver cannot be the requester. There are at most 3 pending requests per cashier (`429 APPROVAL_LIMIT`). An expired request is never approved; expiry is computed when the request is read, so no worker is needed. |
| RA7 | **Audit.** New audit actions: `APPROVAL_REQUESTED`, `APPROVAL_APPROVED`, `APPROVAL_REJECTED`. Consumption is recorded on the action's existing audit row (`VOID_ORDER`, `REFUND`, `STOCK_OVERRIDE`, `SHIFT_CLOSE`) with `approval_id`. |

## 3. Schema (additive migration `20261104000024_approval_requests`)

`approval_requests` has RLS, scoped by `store_id`, and uses the composite foreign keys of migration 000023:

| Column | Type | Notes |
|--------|------|-------|
| `id`, `store_id` | UUID | `UNIQUE (store_id, id)` |
| `action` | VARCHAR(20) | `CHECK IN ('void','refund','stock_override','shift_variance')` |
| `reference_id` | UUID | Order, payment, product or shift |
| `params` | JSONB | `{"new_quantity"}` or `{"counted_cash"}`; `{}` otherwise |
| `amount` | BIGINT | Server-computed: order total, payment amount, variance, or quantity delta |
| `reason` | TEXT NOT NULL | From the cashier |
| `requested_by`, `decided_by` | UUID | FK `users(store_id, id)` |
| `status` | VARCHAR(12) | `CHECK IN ('pending','approved','rejected','consumed')`; "expired" is derived |
| `decision_note` | TEXT | |
| `expires_at`, `decided_at`, `consumed_at`, `created_at` | TIMESTAMPTZ | |

Indexes are `(store_id, status, created_at)` and `(store_id, requested_by, status)`. The migration also
widens `chk_audit_logs_action` with the three new actions, following the 000016 pattern. The down
migration refuses to run while rows exist.

## 4. API contract

| Method & path | Permission | Body → response | Errors |
|---------------|-----------|-----------------|--------|
| `POST /api/v1/pos/approvals` | permission of the action | `{action, reference_id, params?, reason}` → `201 Approval` | 403, 404 reference, 409 `APPROVAL_NOT_NEEDED` (amount within the cashier's own limit), 422, 429 `APPROVAL_LIMIT` |
| `GET /api/v1/pos/approvals/:id` | requester or `approval.decide` | → `200 Approval` | 403, 404 |
| `GET /api/v1/approvals?status=pending` | `approval.decide` | → `200 {items: [Approval]}`, newest first, unexpired | 403 |
| `POST /api/v1/approvals/:id/approve` | `approval.decide` | `{pin}` → `200 Approval` | 401 `INVALID_OWNER_PIN`, 409 `APPROVAL_NOT_PENDING` / `APPROVAL_EXPIRED`, 403 approver is requester, 429 PIN limits |
| `POST /api/v1/approvals/:id/reject` | `approval.decide` | `{note?}` → `200 Approval` | 409 `APPROVAL_NOT_PENDING` |
| void / refund / stock-override / shift close | unchanged | adds optional `approval_id` | 409 `APPROVAL_INVALID` (wrong action, reference, amount or requester; used; expired; not approved) |

`Approval` contains: `id`, `action`, `reference_id`, `reference_label` (order number, product name,
or shift), `amount`, `params`, `reason`, `status` (`pending`, `approved`, `rejected`, `consumed`,
`expired`), `requested_by_name`, `decided_by_name`, `decision_note`, `expires_at`, `decided_at`, and
`created_at`.

## 5. Sequence (void above the threshold)

1. The cashier calls `POST /pos/orders/:id/void` without `owner_pin` and receives `422 OWNER_PIN_REQUIRED` (unchanged).
2. The cashier calls `POST /pos/approvals {action: "void", reference_id: <order>, reason}` and receives `201` (`pending`, `expires_at` = now + 2 minutes).
3. The owner's phone receives the WebSocket `owner` event `approval.requested`, then calls `GET /approvals?status=pending` and shows the order number, amount and reason.
4. The owner calls `POST /approvals/:id/approve {pin}`, which returns `approved`, and the `pos` event `approval.decided` is emitted.
5. The cashier calls `POST /pos/orders/:id/void {reason, pin, approval_id}`. The void and the consumption commit together.

## 6. Consequences

- The owner no longer has to be at the counter. The PIN never leaves the owner's phone.
- An approval cannot be reused for another order, a larger amount, or another cashier.
- If the owner's phone is offline or the app is closed, the request expires after 2 minutes, and the
  tablet `owner_pin` still works.
- Cost: one table, five endpoints, one channel, one permission, and an `approval_id` field on four
  existing requests. No new dependency.

## 7. Not decided here

- Web Push notifications to a closed app (needs a domain and VAPID keys).
- Approval for discounts above `cashier_max_discount_bps`: today that path has no owner PIN at all.
- A `manager` role that could approve.
