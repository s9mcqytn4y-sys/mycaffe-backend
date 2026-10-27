---
name: release-readiness
description: "Checks whether a change is ready for staging or production release and prepares a concise release checklist."
---

# Release readiness workflow

1. Inspect Git status, diff, target branch, and CI configuration.
2. Confirm no secrets, local data, generated binaries, or unrelated changes are included.
3. Run format, lint/typecheck if applicable, unit/integration tests, build, and migration validation. Record exact commands and results.
4. Verify environment-specific configuration and confirm staging uses sandbox payment credentials and non-production data.
5. Check tenant isolation, payment/webhook behavior, order state transitions, logging, and operational monitoring for affected features.
6. Confirm database backup/restore and rollback steps for schema changes.
7. Require explicit approval for production deploy, live payment tests, or irreversible migrations.
8. Provide a go/no-go summary with blockers, risks, and next steps.
