---
trigger: glob
description: "Standar implementasi untuk source code backend Go."
globs: "**/*.go"
---

# Go backend rules

- Inspect the existing `go.mod` and package structure before adding dependencies or choosing Fiber/Echo/another router.
- Follow idiomatic Go: exported names use `PascalCase`, local names use `camelCase`, packages use short lowercase names, and files use lowercase names with underscores only when useful.
- Run `gofmt` on changed Go files. Keep functions focused and avoid speculative interfaces or unnecessary abstractions.
- Pass `context.Context` through database and network calls; set sensible request and upstream timeouts.
- Wrap errors with context, but do not leak internal errors, SQL details, credentials, or stack traces to clients.
- Validate request bodies, path/query parameters, enums, lengths, numeric ranges, and tenant/store access at the server boundary.
- Use parameterized SQL or safe query builders. Never concatenate user input into SQL.
- Do not put business rules in handlers if they belong in services/domain logic. Do not make handlers the source of truth for price/payment calculations.
- Avoid global mutable state for request-scoped or tenant-scoped data.
- Add unit/integration tests for business behavior and error paths. Mock external payment calls in automated tests.
- Do not add a dependency solely for convenience without checking maintenance status, license, size, and whether the standard library or an existing dependency is sufficient.
