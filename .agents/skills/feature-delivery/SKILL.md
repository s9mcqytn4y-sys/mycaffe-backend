---
name: feature-delivery
description: "Implements a backend feature end to end with a small plan, tests, documentation, and risk review. Use when the owner asks to build or modify a feature."
---

# Feature delivery workflow

1. Read the relevant product requirement and architecture decision documents.
2. Inspect the repository structure, existing patterns, `go.mod`, current Git changes, and tests. Do not assume a framework or schema that has not been verified.
3. Write a short implementation plan listing affected packages, API/schema changes, security considerations, tests, and rollback needs.
4. Implement the smallest coherent change. Keep pricing, authorization, payment state, and tenant isolation in the backend.
5. Add or update tests for success, validation failures, permission failures, concurrency, and relevant edge cases.
6. Run formatting and focused tests; run broader checks if feasible. Never claim success without command output.
7. Update API/product documentation and migrations where needed.
8. Summarize files changed, behavior delivered, tests run, unresolved risks, and any manual steps.
9. Do not deploy or run destructive database changes unless the owner explicitly authorizes them.
