---
name: database-migration
description: "Designs, reviews, and tests PostgreSQL schema changes and migrations safely."
---

# Database migration workflow

1. Inspect the existing schema, migration tooling, naming conventions, tenant relationships, and query patterns.
2. Explain why the schema change is needed and identify affected data and application versions.
3. Prefer an additive migration with constraints and indexes. If data backfill is needed, describe batching, locking, and retry behavior.
4. Keep SQL versioned in the repository. Do not use a dashboard-only change as the sole source of truth.
5. Consider tenant isolation, foreign keys, uniqueness under concurrency, timezone semantics, auditability, and how historical order snapshots are preserved.
6. Provide rollback guidance and identify irreversible steps.
7. Test against a disposable development database if available. Never reset or mutate production.
8. Report what was tested and what remains unverified.
