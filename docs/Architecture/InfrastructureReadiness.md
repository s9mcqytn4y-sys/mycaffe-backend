# Infrastructure Readiness & Architectural Boundaries

## 1. Hosting Tier & Production Topology (ADR-002)

- **Production Server**: Single-VPS Hostinger KVM 2 (2 vCPU, 8 GB RAM, 100 GB NVMe SSD, Ubuntu 24.04 LTS).
- **Deployment Model**: Modular Monolith orchestrated with Docker Compose (`deploy/docker/docker-compose.prod.yml`):
  - `tunnel`: Named Cloudflare Tunnel, the only ingress (IN1, P0-07); TLS at the Cloudflare edge, no published ports.
  - `nginx`: Internal reverse proxy on port 80 (no published ports), visitor IP from `CF-Connecting-IP`, rate limiting (30 rps general, 10 rps webhook), WebSocket upgrades (`/ws`), and gzip compression.
  - `migrate`: One-shot Go migration service (`/app/mycaffe-migrate -action=up`) running as non-superuser `mycaffe_migrator`.
  - `api`: Go backend service (`cmd/api`), exposed internally on port 8080, running as least-privilege `mycaffe_app` (`NOSUPERUSER NOBYPASSRLS`).
  - `web`: Standalone Next.js PWA, exposed internally on port 3000.
  - `postgres`: Native PostgreSQL 18-alpine, internal port 5432 with persistent volume `pgdata:/var/lib/postgresql`.
- **Zero In-Place Coding Rule**: Source code is never edited or compiled on the production VPS. All releases are built in GitHub Actions and pulled from `ghcr.io` as immutable tagged images.

## 2. Hostinger API & MCP Boundary

- **Scope Definition**: The Hostinger API and Hostinger MCP (`https://mcp.hostinger.com`) are strictly infrastructure management tools (used for server health monitoring, DNS record management, and VPS maintenance operations).
- **Hard Rule**: Hostinger API/SDK must never be imported as an application runtime dependency inside the Go backend codebase (`mycaffe-backend`).
- **Access Control**: Any future infrastructure provisioning or DNS change via MCP requires explicit owner approval and must never be automated autonomously by agents without review.

## 3. Database Architecture & Realtime Strategy

- **Runtime Target**: Native PostgreSQL 18 (pinned to `postgres:18-alpine` in Docker and CI).
- **Decoupling from External DB PaaS**: External database PaaS runtime dependencies are fully superseded (ADR-002). The platform runs on pure PostgreSQL 18 with versioned SQL migrations (`db/migrations/`) and development seeds (`db/seeds/development.sql`).
- **Realtime Layer**: Real-time event propagation across KDS, POS, and TV queue display screens is handled by the Go WebSocket Hub (`internal/realtime`) paired with transactional `outbox_events`, scoped strictly by tenant `store_id`.
- **Resource Tuning (8 GB RAM VPS)**:
  - `shared_buffers = 1GB`
  - `effective_cache_size = 3GB`
  - `work_mem = 16MB`
  - `maintenance_work_mem = 256MB`
  - `checkpoint_completion_target = 0.9`

## 4. Payment Gateway & Reconciliation Readiness

- **Midtrans Core API**: Direct Core API (`qris`, `bank_transfer`, `gopay`, `shopeepay`, `card`, `cstore`) plus cashier offline methods (`cash`, `static_qris`, `edc`) without Snap popup dependencies.
- **MDR & Tax Configuration**:
  - Regular QRIS MDR, PB1 restaurant tax, and service charge are stored in Basis Points (`BPS`) per tenant on `stores` (`tax_bps`, `service_bps`, `qris_mdr_bps`).
- **Fail-Closed Baseline**: Public HTTP order creation (`POST /api/v1/orders`) and WebSocket (`/ws`) return `503 Service Unavailable` in `staging` and `production` until signed table QR / staff JWT middleware is bound on public routes.
- **Webhook Reconciliation**: Verifies SHA-512 signature over raw `gross_amount`, validates integer Rupiah amount and status mapping, then reconciles the payment ledger in one `WithTenantTx`. Returns `503 Service Unavailable` (`Retry-After: 60`) only when the commit fails, so Midtrans retries.
- **Webhook Idempotency**: Enforced via unique index `uq_payment_notification_logs_event_hash` on `(store_id, event_hash)`, where `event_hash` is SHA-256 over provider order ID, transaction ID, status code, transaction/fraud status, and gross amount (migration `000004`).

## 5. Implementation Status (4-Tier Matrix)

- **Implemented & Tested**:
  - `deploy/docker` & `deploy/nginx`: Multi-stage Dockerfile (`api` + `migrate`), dev & prod Docker Compose, Nginx TLS bootstrap entrypoint, and Let's Encrypt script.
  - `db/migrations` & `cmd/migrate`: PostgreSQL 18 schema (17 tables) with triple-role separation (`postgres`, `mycaffe_migrator`, `mycaffe_app`), RLS (`ENABLE ROW LEVEL SECURITY`), `ON DELETE RESTRICT` + `REVOKE DELETE` on financial history, and additive `orders.request_hash` migration.
  - `internal/pkg/database`: Safe URL builder, `sqlx` connection pool, `WithTenantTx` RLS context wrapper, and `pg_advisory_lock` migrator.
  - `internal/service/pricing.go`: Pure integer-Rupiah (`int64`) & Basis Points (`BPS`) catalog validation and pricing engine with 1-level nested sub-addons (`OptionModifierGroup`).
  - `internal/repository/postgres` & `internal/service/order_service.go`: Transactional order persistence inside `WithTenantTx` with sorted `FOR UPDATE` stock locking, `idempotency_key` + `request_hash` conflict detection, atomic `store_daily_sequences` allocation, and `outbox_events` insertion.
  - `internal/config`, `internal/pkg/auth`, `internal/pkg/midtrans`, `internal/realtime`: Fail-closed config, JWT validator, Midtrans Core API client & status mapper, and tenant-scoped WebSocket hub.
- **Guarded / Fail-Closed**:
  - `POST /api/v1/orders`: Returns `503 Service Unavailable` in `staging` and `production` until authenticated tenant middleware is wired on the HTTP route.
  - `GET /ws`: Returns `503 Service Unavailable` in `staging` and `production` until Phase 03 session ticket lifecycle is active.
  - `POST /api/v1/pos/orders` and `POST /api/v1/pos/orders/{id}/payments` (Phase 02.01-C): open to authenticated staff with the matching RBAC permission; store and actor come from the token.
- **Implemented & Tested (Phase 01 Sub-task 04)**:
  - Payment initiation (cash/static QRIS/EDC + Midtrans Core API two-phase charge), durable webhook inbox, duplicate-settlement detection, order expiry and reconciliation worker (`internal/service/payment_service.go`).
- **Planned**:
  - Phase 02: Staff authentication, RBAC middleware, signed table QR, and guest table sessions — see `docs/Architecture/Roadmap.md`.
- **Blocked by Dependency**:
  - Production Midtrans credentials, domain/DNS cutover, and live Hostinger KVM 2 VPS provisioning.

## 6. Backup & Disaster Recovery

- **Offsite Storage Destination**: Dual-bucket Cloudflare R2 (`mycaffe-media` for public CDN assets and `mycaffe-backups` for private zero-egress database backups).
- **Automated Script**: `scripts/backup/backup_to_r2.sh` executes `pg_dump`, compresses and encrypts the archive using GPG (`--cipher-algo AES256 --digest-algo SHA512`), generates a `.sha256` checksum manifest, and uploads both to `mycaffe-backups`.
- **Restore Verification**: `scripts/backup/restore_from_r2.sh` downloads the encrypted backup and `.sha256` manifest, verifies checksum integrity before decryption, and restores into PostgreSQL 18.
