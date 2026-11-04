# MyCaffe Core POS: Gemini & Antigravity Instructions

## Repository Role
This repository (`mycaffe-backend`) is the Go backend for MyCaffe, a configurable, multi-tenant QR table ordering, POS, KDS, and TV queue display platform. The frontend is a separate Next.js application; do not generate or manage frontend code within this backend repository. Official brand guidelines, visual tokens (*Espresso & Crema*), and product knowledge are codified in [docs/Product/BrandAndProductKnowledge.md](docs/Product/BrandAndProductKnowledge.md).

## Guiding Principles & Quality Standards
1. **Multi-Tenant Security & RLS**: Every query, mutation, and realtime event must be strictly scoped by the authenticated `store_id` (tenant UUID) inside `database.WithTenantTx` using PostgreSQL 18 `ENABLE ROW LEVEL SECURITY` and the non-owner runtime role `mycaffe_app` (`NOBYPASSRLS`). Only narrowly-scoped `SECURITY DEFINER` functions owned by `mycaffe_migrator` (`resolve_payment_webhook_context` after Midtrans SHA-512 verification , `list_stores_with_pending_reconciliation` for background worker tenant discovery, , `resolve_store_for_login` returning only `(store_id, is_active)` for staff login, and `list_stores_with_due_email` for email outbox delivery, and `resolve_table_qr` returning only `(store_id, table_id, is_active)` for a scanned table QR, `track_order_by_token` returning only minimal order status for the public tracker, `list_stores_with_pending_outbox` returning only store ids for the outbox relay) may resolve tenant UUIDs prior to `database.WithTenantTx`. Never trust client-provided tenant parameters on public routes.
2. **Payment Integrity & 2-Phase Gateway Lifecycle**: Prices, modifiers, 1-level nested sub-addons, discounts, taxes, and service charges are calculated exclusively on the server from the official catalog. Money amounts must be whole integer Indonesian Rupiah (`int64` / PostgreSQL `BIGINT`) without floating-point math or fractional cents. Midtrans Core API charges must use `payments.id.String()` as both `provider_order_id` and Midtrans `Idempotency-Key` in a 2-phase transaction (never holding DB locks during external HTTP calls) with `CheckTransaction` fallback reconciliation.
3. **Fail-Closed Operations & WebSocket Lifecycle**: In `staging` and `production`, endpoints lacking complete authenticated tenant binding or durable database persistence must fail-closed (`503 Service Unavailable`). WebSocket connections (`internal/realtime/hub.go`) must enforce origin validation, tenant-isolated broadcast channels, read size limits, ping/pong heartbeat deadlines, and context-aware graceful shutdown.
4. **Single-VPS Modular Monolith**: Production deployment runs on a single Hostinger KVM 2 VPS (Ubuntu 24.04 LTS) via Docker Compose (`postgres:18-alpine`, Go API backend, Next.js frontend, Nginx reverse proxy). External database PaaS runtime dependencies are fully superseded (ADR-002).
5. **Cloudflare R2 Dual-Bucket Storage**: Cloudflare R2 is used with two isolated buckets and separate credentials: `mycaffe-media` (public/CDN product & store media) and `mycaffe-backups` (private GPG AES256+SHA512 encrypted `pg_dump` backups with zero egress fees).
6. **Zero In-Place Coding on Production VPS**: Never edit source code, SQL migrations, or run `go build` directly on the production VPS. All changes must pass through `feature/*` -> `staging` CI -> `main` -> immutable GHCR container image deployment.

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
