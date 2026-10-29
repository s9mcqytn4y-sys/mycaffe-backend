---
trigger: model_decision
description: "Aktif saat merancang schema, query, authorization, store settings, catalog, staff, atau laporan multi-tenant."
---

# Multi-tenant and data-integrity rules

- Treat the product as a multi-tenant white-label system. Store name, logo, theme, menu, modifier groups, pricing rules, receipt layout, tax/service settings, table QR tokens, staff, and operating policy must be data-driven per store.
- Before adding a tenant-owned table, decide how it relates to `stores`/`tenants`; use the existing naming and schema if already established.
- Every tenant-owned row and query must be scoped to the authenticated store context. A `store_id` sent by the browser is a selector, not proof of authorization.
- Design and test cross-tenant access denial. Use database constraints and PostgreSQL 18 Row-Level Security (`ENABLE ROW LEVEL SECURITY`) with `database.WithTenantTx` and the non-superuser runtime role (`mycaffe_app` with `NOBYPASSRLS`).
- Use UUIDs or the established internal ID strategy for database identity. Human-readable order/queue numbers are display identifiers, not primary keys.
- Generate table QR tokens with cryptographically secure randomness, validate them on the server, support revocation/rotation, and never rely on a browser-stored table number as proof that the user is physically at that table.
- Store immutable order-line snapshots for purchased name, unit price, modifiers, fees/taxes, discount, and relevant cost basis.
- Use database transactions and constraints for stock/order/payment invariants. Handle concurrent ordering and queue-number generation safely in the database, not with an in-memory counter.
- Do not silently migrate real tenant data or run destructive database actions.
