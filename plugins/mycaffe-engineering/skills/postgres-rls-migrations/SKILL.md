---
name: postgres-rls-migrations
description: Plan and review MyCaffe PostgreSQL 18 schema migrations, tenant composite constraints, RLS policies, rollback, and integration tests.
disable-model-invocation: true
---

# MyCaffe PostgreSQL 18 migration workflow

## Before editing
- Read CLAUDE.md, AGENTS.md, docs/architecture/phase-01-database-contract.md, docs/architecture/decisions-before-code.md, all relevant migration up/down files, database.WithTenantTx, current RLS helpers, scripts/verify, seed files, and CI workflow.
- Confirm the latest applied migration and checksum behavior. Never edit an applied or merged migration; add a new migration.
- Identify every tenant-owned table, composite foreign key, unique constraint, trigger, index, RLS policy, grant, and security-definer function affected.

## Required design checks
- Use PostgreSQL 18-compatible SQL.
- Tenant-owned tables must carry store_id and enforce store-scoped references with composite foreign keys where applicable.
- Runtime role mycaffe_app must remain NOBYPASSRLS and must not own tenant tables.
- Use database.WithTenantTx and current_store_id() for tenant operations.
- SECURITY DEFINER functions must be narrowly scoped, owned by the intended migrator role, use a fixed safe search_path, expose minimal outputs, and have explicit EXECUTE grants.
- Include reversible down migration behavior. Do not drop or rewrite historical data without explicit approval.
- Keep secrets and real customer/payment data out of seeds and tests.
- Keep test fixtures deterministic and seed scripts idempotent.

## Verification
Run or request:
- Fresh PostgreSQL 18 migration up.
- Schema constraints and RLS verification as mycaffe_app.
- Cross-tenant read/write denial.
- Migration rollback and re-apply.
- Seed twice with before/after row-count comparison.
- Go integration tests with the race detector.
- Docker Compose validation.

Do not execute destructive SQL, modify production data, or connect to production. Do not edit migrations or run database mutations until the user approves the migration plan.
