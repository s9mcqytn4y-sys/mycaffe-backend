---
name: phase02-auth-rbac
description: Review or design MyCaffe staff authentication, session lifecycle, and RBAC for the existing four roles. Use before implementing Phase 02 authentication or permission changes.
disable-model-invocation: true
---

# Phase 02 — Staff Authentication and RBAC

## Required source review
Read CLAUDE.md, AGENTS.md, the Go backend and multi-tenant rules, docs/product/module-3-admin-backoffice.md, docs/architecture/phase-01-database-contract.md, docs/architecture/pos-use-cases-and-sequences.md, docs/architecture/decisions-before-code.md, internal/domain/store.go, internal/pkg/auth/token.go, internal/config/config.go, cmd/api/main.go, the current user migration, and relevant handler/service tests.

## Inspect before proposing
- Confirm the current users schema, User model, role enum, JWT claims, config defaults, route wiring, RLS policies, audit constraints, and seed fixtures.
- Identify whether each value comes from a trusted token, database row, route parameter, or untrusted request body.
- Compare documentation with implementation and report drift instead of silently resolving it.
- Preserve the four existing roles: owner, cashier, barista, kitchen. Do not introduce a manager, admin, customer, or custom role without approval.

## Required security design
- Derive store_id and actor user identity from verified server-side authentication context. Remove request-body identity fields from authenticated HTTP contracts; do not merely ignore them in one handler.
- Use a single authoritative permission policy in the backend. Enforce it in middleware and re-check critical business invariants in services.
- Revalidate active staff status and current role/session state so deactivation or role changes cannot leave long-lived JWT privileges active.
- Separate login authentication from action-level PIN confirmation. Do not assume the existing pin_hash has a production hashing implementation; inspect seed format and code first.
- Specify login identifier, PIN/password policy, hash algorithm, rate limits, lockout/backoff, refresh-token rotation, logout, revocation, session expiry, and audit events.
- Use generic authentication failures to avoid user/store enumeration. Never log credentials or tokens.
- Any pre-tenant store lookup must use a narrowly scoped, reviewed resolver or public lookup contract; never bypass RLS broadly.

## Required deliverable
Produce:
1. Current-state findings with file paths.
2. Threat model and trust-boundary diagram.
3. Proposed login and refresh/logout API contracts.
4. A permission matrix for the four existing roles and guest sessions.
5. Proposed session and migration model with composite tenant constraints and RLS.
6. Abuse cases and table-driven tests, including cross-tenant IDOR and role-change revocation.
7. Open product decisions and rollback plan.

Do not edit code, create migrations, change role definitions, or issue tokens until the user explicitly approves the design and implementation scope.
