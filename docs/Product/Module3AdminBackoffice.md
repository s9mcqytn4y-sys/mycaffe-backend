# Module 3: Owner/Admin Backoffice

## Primary goals

Single source of truth, fraud prevention, and decisions grounded in reconciled data. The dashboard should communicate operational health quickly without confusing gross sales, gateway settlement, estimated gross profit, and net profit.

## Pillars

1. **Dashboard:** active shift, live queue load, SLA, sold-out alerts, gross/net sales, transaction count, AOV, payment-method split, and filters by period.
2. **Catalog and modifiers:** products, categories, centralized modifier groups/options, required/optional selection limits, selling price, optional cost basis, availability, schedule, and sold-out reset policy.
3. **Finance:** cost/HPP, margin estimates, gateway settlement reconciliation, store-configured tax/service/takeaway rules, expenses, and exportable P&L.
4. **Analytics:** menu-engineering matrix, hourly sales, table turnover/revenue, and preparation SLA by station/shift.
5. **Staff/RBAC:** 4 streamlined operational roles (`owner`, `cashier`, `barista`, `kitchen`) with least privilege and fast-action PIN + mandatory reason logging to `audit_logs` (without multi-step manager approval bottlenecks).
6. **Audit logs:** actor, action, target, timestamp, reason, approval actor, and structured details for void/refund/price/stock/shift/role changes.
7. **Brand/settings:** store identity, logo, theme, menu banners, receipt builder, Wi-Fi details if the owner chooses to print them, and configurable operating policies.
8. **Table QR management:** generated table tokens, active/inactive state, rotation/revocation, branded print-ready QR templates, and emergency QR-order pause.

## Finance correctness

- Treat sample tax/MDR percentages and settlement times as examples only. Configure them per store/provider and verify current provider terms and applicable rules.
- Preserve the actual fee/tax/discount rules applied to each transaction so later setting changes do not rewrite historical sales.
- Gross profit requires valid cost/HPP data. Net profit requires complete operating expenses and cost treatment; label partial data as an estimate.
- Reconcile gateway gross amount, provider fees, expected net settlement, settlement status, and bank evidence without counting the same transaction twice.
- Do not call an order “completed” merely because a payment settled; payment and fulfillment states are separate.

## Security and operational control

- Enforce RBAC on the backend, not only by hiding UI buttons.
- Hash PINs using a strong password-hashing function, rate-limit attempts, apply lockout/backoff, and audit sensitive actions.
- Do not permit a cashier to void/refund a paid transaction without the configured authorization flow.
- Every catalog/order/expense/report query must be scoped to the current store/tenant.
- Rotate table tokens without changing internal table identity; revoked QR codes must stop accepting new orders.
- Keep owner-only financial and cost fields out of cashier/kitchen APIs.

## Suggested entities to evaluate (not a final schema)

`stores`, `store_settings`, `tables`, `products`, `categories`, `modifier_groups`, `modifier_options`, `product_modifier_groups`, `orders`, `order_items`, `payments`, `pos_shifts`, `expenses`, `staff_users`, `audit_logs`, and appropriate settlement/reconciliation records.

Review existing schema and the architecture decision document before creating any of these. Do not blindly create duplicate tables if equivalent structures already exist.
