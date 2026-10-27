---
trigger: glob
description: "Standar perubahan schema dan migration database."
globs: "**/*.sql, **/migrations/**"
---

# Database and migration rules

- Inspect the current schema and migration tool before writing SQL. Follow the project's existing migration naming/version convention.
- Prefer small, reviewable, additive migrations. Explain any destructive or data-rewriting migration and require explicit approval before running it against any shared or production database.
- Include primary keys, foreign keys, appropriate unique constraints, check constraints, and indexes based on actual query paths.
- Add indexes for tenant/store-scoped access and common operational queries only after considering selectivity and write overhead.
- Keep timestamps consistent and define the business timezone policy for daily queue numbers, shifts, and reports.
- Use transactions where supported. Avoid migrations that depend on undocumented manual dashboard steps.
- Provide rollback guidance; if a truly reversible migration is not possible, say so.
- Test fresh schema creation and upgrade from the previous schema where practical.
- Do not embed credentials, production connection strings, customer data, or real tokens in migration files.
