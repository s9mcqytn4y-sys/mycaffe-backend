# Kafe Senja Vibe — Gemini & Antigravity Instructions

## Repository Role
This repository (`mycaffe-backend`) is the Go backend for Kafe Senja Vibe — a configurable, multi-tenant QR table ordering, POS, KDS, and TV queue display platform. The frontend is a separate Next.js application; do not generate or manage frontend code within this backend repository.

## Guiding Principles & Quality Standards
1. **Multi-Tenant Security**: Every query, mutation, and realtime event must be strictly scoped by the authenticated `store_id` (tenant UUID). Never trust client-provided tenant parameters.
2. **Payment Integrity**: Prices, modifiers, discounts, taxes, and service charges are calculated exclusively on the server from the official catalog. Money amounts must be whole integer Indonesian Rupiah (`int64` / PostgreSQL `BIGINT`) without floating-point math or fractional cents.
3. **Fail-Closed Operations**: In production (`APP_ENV=production`), all endpoints must fail-closed if required configuration is missing or insecure.
4. **Free-Tier Governance**: All development, staging, CI, and test services operate strictly within Free-Tier limits (Supabase Free, Midtrans Sandbox, Cloudflare R2 Free, Sentry Developer).

## Bounded Autonomy for Agents
- **Autonomous Decisions (No confirmation required)**:
  - Adding new unit/integration tests and expanding test coverage.
  - Fixing linter warnings, typos, and formatting (`gofmt`).
  - Adding defensive validation, input normalization, and edge-case error masking.
  - Internal refactoring that preserves existing public API contracts.
  - Creating or updating technical documentation, ADRs, and tests.
- **Approval Gate (Explicit confirmation required)**:
  - Destructive or breaking database schema changes.
  - Altering public API request/response contracts or WebSocket payload formats.
  - Modifying authentication/authorization models or role definitions.
  - Adding external dependencies (especially heavy or CGO-dependent libraries).
  - Modifying production deployment configurations or infrastructure manifests.
  - Editing live `.env` files or rotating credentials.
- **Handling Uncertainty**:
  - Inspect codebase, tests, and documentation before asking questions.
  - For low-risk, reversible decisions, choose a conservative path, document assumptions, and proceed.
  - Escalate only unresolved high-impact decisions affecting security, data integrity, contracts, or cost.

## Command Execution & Verifications
- Run `gofmt -l .`, `go vet ./...`, and `go test -v -race ./...` before reporting completion.
- Run `govulncheck ./...` with pinned version to verify dependency security.
- Scan for leaked credentials with `gitleaks git --verbose`.
- Never claim a build, test, migration, or CI succeeded without inspecting actual command output.
