# Infrastructure Readiness & Architectural Boundaries

## 1. Hosting Tier & Production Topology (ADR-002)
- **Production Server**: Single-VPS Hostinger KVM 2 (2 vCPU, 8 GB RAM, 100 GB NVMe SSD, Ubuntu 24.04 LTS).
- **Deployment Model**: Modular Monolith orchestrated with Docker Compose:
  - `nginx`: Reverse proxy (ports 80/443), TLS termination (Let's Encrypt Certbot), rate limiting (30 rps general, 10 rps webhook), WebSocket upgrades (`/ws`), gzip compression.
  - `api`: Go backend service (`cmd/api`), exposed internally on port 8080.
  - `web`: Standalone Next.js PWA, exposed internally on port 3000.
  - `postgres`: Native PostgreSQL 18-alpine, internal port 5432 with persistent volume `pgdata`.
- **Vercel Hobby Plan Compliance**: By hosting the Next.js standalone container on the same VPS behind Nginx, commercial compliance issues with Vercel Hobby are completely eliminated at zero additional cost.

## 2. Hostinger API & MCP Boundary
- **Scope Definition**: The Hostinger API and Hostinger MCP (`https://mcp.hostinger.com`) are **strictly infrastructure management tools** (used for server health monitoring, DNS record management, and VPS maintenance operations).
- **Hard Rule**: Hostinger API/SDK must **never** be imported as an application runtime dependency inside the Go backend codebase (`mycaffe-backend`).
- **Access Control**: Any future infrastructure provisioning or DNS change via MCP requires explicit owner approval and must never be automated autonomously by agents without review.

## 3. Database Architecture & Realtime Strategy
- **Runtime Target**: Native PostgreSQL 18 (pinned to `postgres:18-alpine` in Docker and CI).
- **Decoupling from Supabase**: Supabase runtime dependencies have been superseded (ADR-002). The platform runs on pure PostgreSQL with standard SQL migrations (`db/migrations/`) and seeds (`db/seeds/`).
- **Realtime Layer**: Real-time event propagation across KDS, POS, and TV queue display screens is handled directly by the in-memory Go WebSocket Hub (`internal/realtime`), scoped strictly by tenant `store_id`. No reliance on external database CDC or paid subscription tiers.
- **Resource Tuning (8 GB RAM VPS)**:
  - `shared_buffers = 1GB`
  - `effective_cache_size = 3GB`
  - `work_mem = 16MB`
  - `maintenance_work_mem = 256MB`
  - `checkpoint_completion_target = 0.9`

## 4. Payment Gateway & Reconciliation Readiness
- **Midtrans Core API**: Direct API is selected for dynamic QRIS and Virtual Account payments (without Snap redirect/popup).
- **MDR & Tax Configuration**:
  - Regular QRIS MDR is 0.7% + PPN (VAT) on the transaction fee, subject to merchant agreement.
  - Store PB1 tax (restaurant tax) and service charge must remain configurable per tenant.
- **Fail-Closed Baseline**: Order creation and payment charge initiation are blocked in production until the database repository module validates catalog prices, stock availability, and persists the order before contacting Midtrans.
- **Webhook Reconciliation**: Must verify SHA512 signature, check duplicate event logs, and update payment state inside an ACID database transaction before acknowledging HTTP 200 OK to Midtrans.
- **Webhook Idempotency**: Stored in `payment_notification_logs` with a unique constraint on `(provider, transaction_id, order_number, status_code, transaction_status)`.

## 5. Implementation Status & Boundary Classification
- **Implemented**:
  - `deploy/docker`: Multi-stage Dockerfile and production `docker-compose.yml` for Hostinger KVM 2.
  - `deploy/nginx`: Nginx reverse proxy configuration with WebSocket upgrades and rate limiting.
  - `db/migrations`: PostgreSQL 18 migration schema with `ON DELETE RESTRICT` on financial records, neutral naming, and atomic sequence tracking.
  - `db/seeds`: Development seed data cleanly isolated from production.
  - `internal/config`: Fail-closed validation for production environment, strict boolean/integer parsing, HTTPS origins whitelist.
  - `internal/pkg/auth`: Cryptographic JWT validator enforcing HS256, expiration, tenant store_id UUID, user_id UUID, and role whitelist.
  - `internal/realtime`: Multi-tenant gorilla/websocket hub scoped by store UUID.
- **Guarded / Simulation**:
  - `POST /api/v1/orders`: Simulates price calculation without database persistence.
  - `POST /api/v1/payments/midtrans/webhook`: Verifies signature; responds 503 to force gateway retry until ACID persistence is active.
- **Planned (Phase 1 Database Layer)**:
  - Database connection pool using `sqlx` and `jackc/pgx/v5/stdlib`.
  - Durable persistence in webhook handler inserting to `payment_notification_logs`.
  - Catalog & menu management with stock locks and atomic daily sequence generation.

## 6. Backup & Disaster Recovery
- **Offsite Storage Destination**: Cloudflare R2 bucket (`mycaffe-backups`) with zero egress fees.
- **Automated Script**: `scripts/backup/backup_to_r2.sh` executes `pg_dump -Fc`, encrypts the binary payload with AES-256-CBC using OpenSSL and PBKDF2, and uploads to R2 via S3 API.
- **Retention**: Daily automated backups retained for 30 days. Periodic disaster recovery drill restores backups onto a staging database to verify recovery integrity.
