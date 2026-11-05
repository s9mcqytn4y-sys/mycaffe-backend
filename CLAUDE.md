# CLAUDE.md — MyCaffe Core POS Backend (`mycaffe-backend`)

## Project Overview
`mycaffe-backend` is the Go 1.27 modular monolith backend for **MyCaffe**, a configurable, multi-tenant QR table ordering, Point of Sale (POS), Kitchen Display System (KDS), TV queue display, and owner backoffice platform for cafes and restaurants.
- **Tech Stack**: Go 1.27, Gin Gonic (`github.com/gin-gonic/gin`), `sqlx` + `pgx/v5`, PostgreSQL 18 (`ENABLE ROW LEVEL SECURITY`), Midtrans Core API (direct HTTP, no Snap), Cloudflare R2 (dual-bucket S3 API), Sentry Go SDK, Prometheus metrics.
- **Frontend**: Separate Next.js App Router + TypeScript + Tailwind CSS/PWA repository. Never create frontend code in this repository.
- **Domain**: `abbdullaziz.online` (`https://demo.abbdullaziz.online` for Demo/Staging via Cloudflare Named Tunnel `mycaffe-demo`, `https://cdn.abbdullaziz.online` for R2 public media).

## Essential Commands
```powershell
# Format, lint, and static analysis
gofmt -s -w .
go vet ./...

# Unit & race-detector test suite
go test -v -race ./...

# Full PostgreSQL 18 integration test suite (requires Docker)
./scripts/dev.ps1 test-integration

# Local development stack & migrations
./scripts/dev.ps1 up
./scripts/dev.ps1 migrate-up
./scripts/dev.ps1 seed
./scripts/dev.ps1 run

# Local Demo / Staging stack (https://demo.abbdullaziz.online)
docker compose --env-file .env.demo -f deploy/docker/docker-compose.demo.yml up -d --build
```

## Non-Negotiable Architecture & Security Invariants
1. **PostgreSQL 18 Row-Level Security (RLS) & Triple-Role Separation**:
   - Runtime queries execute as `mycaffe_app` (`NOBYPASSRLS`) inside `database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error { ... })`.
   - Schema migrations and seeds execute as `mycaffe_migrator` via `cmd/migrate`.
   - Only approved, narrowly-scoped `SECURITY DEFINER` functions owned by `mycaffe_migrator` (`resolve_store_for_login`, `resolve_payment_webhook_context`, `resolve_table_qr`, `track_order_by_token`, `list_stores_with_pending_reconciliation`, `list_stores_with_open_gateway_refunds`, `list_stores_with_due_email`, `list_stores_with_due_message`, `list_stores_with_pending_outbox`, `list_stores_with_due_table_timers`) may resolve tenant context before `database.WithTenantTx`.
2. **Money & Pricing Integrity**:
   - All money values use whole integer Indonesian Rupiah (`int64` in Go, `BIGINT` in PostgreSQL). Never use `float32`/`float64` for currency.
   - Calculate all prices, variants, modifier groups (and 1-level child modifier groups), discounts, service charges, and PB1 taxes server-side from catalog snapshots.
3. **Separated Order & Payment State + 2-Phase Midtrans Core API**:
   - `order_status` and `payment_status` are strictly separate lifecycles.
   - Midtrans Core API charges (`qris`, `gopay`, `shopeepay`) use a 2-phase transaction (`payments.id.String()` as `provider_order_id` and `Idempotency-Key`), never holding a database transaction open across external HTTP calls.
   - Verify Midtrans webhooks with SHA-512 (`order_id + status_code + gross_amount + ServerKey`) before mutating payment state.
4. **Immutable Migrations**:
   - Never edit an existing migration in `db/migrations/` after it is merged (`schema_migrations` enforces SHA-256 checksums). Always add a new numbered `.up.sql` / `.down.sql` pair.
5. **Secret Hygiene & Zero In-Place VPS Coding**:
   - Never read, print, or commit `.env` or `.env.demo` secrets. Only update `.env.example` and `.env.demo.example` with safe placeholders.
   - Never compile or edit source files directly on the production VPS. Follow `feature/*` -> `staging` -> `main` -> GHCR digest deployment.

## Repository Layout
- `cmd/api/` — HTTP API server entrypoint (`main.go`) and handler/route wiring (`routes.go`).
- `cmd/migrate/` — Database migration, seed, demo-PIN rotation, and store bootstrap CLI.
- `internal/config/` — Fail-closed environment configuration and validation.
- `internal/domain/` — Pure domain structs, constants, and status enums (aligned 1:1 with PostgreSQL `CHECK` constraints).
- `internal/handler/` — Gin HTTP handlers, middleware (`RequestID`, `AccessLog`, `Recover` with Sentry PII scrubbing, `LimitBody`).
- `internal/service/` — Business logic, pricing engine, 2-phase payment lifecycle, outbox workers (`email`, `sms`, `realtime`, `reconciliation`).
- `internal/repository/postgres/` — `sqlx` PostgreSQL repositories executed inside `database.WithTenantTx`.
- `internal/realtime/` — Tenant-isolated WebSocket hub (`hub.go`, `client.go`).
- `internal/pkg/` — Focused infrastructure adapters (`database`, `jwt`, `mailer`, `metrics`, `midtrans`, `money`, `pin`, `rbac`, `receipt`, `sms`, `storage`).
- `db/migrations/` — 36 sequential PostgreSQL 18 migrations (`000001`–`000036`); never edit a merged one.
- `docs/` — Product (`docs/Product/`), Architecture & ADRs (`docs/Architecture/`), Setup (`docs/Setup/`), and Engineering (`docs/Engineering/`) documentation.

