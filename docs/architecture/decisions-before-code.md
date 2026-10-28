# Architecture Decisions & Resolution Log

This document records architectural decision gates and their approved resolutions.

## 1. Production Database vs Realtime
- **Decision**: **Option B — Native PostgreSQL 18 with In-Memory Go WebSocket Hub** (Resolved in [ADR-002](adr-002-single-vps-docker-topology.md)).
- **Rationale**: A single Hostinger KVM 2 VPS (2 vCPU, 8 GB RAM, 100 GB SSD) runs PostgreSQL 18 and the Go backend inside Docker Compose. Real-time updates for KDS, POS, and TV queue display are delivered via the authenticated Go WebSocket Hub (`internal/realtime`), scoped strictly by tenant `store_id`. This eliminates external PaaS dependencies and recurring cloud subscription costs.

## 2. Static QRIS vs Dynamic QRIS
- **Decision**: **Dynamic Gateway QRIS via Midtrans Core API** is the primary automated payment flow.
- **Rules**: Static store QRIS is treated strictly as an offline/manual cashier-reconciliation method; automated order status transitions are prohibited for static QRIS unless a verified bank mutation callback is active. Dynamic QRIS initiates a dedicated transaction linked to `order_number` and transitions state upon verified gateway notification.

## 3. Payment State vs Preparation State
- **Decision**: Modeled as two completely independent state machines in the database schema:
  - `orders.status`: `pending_payment`, `queued`, `preparing`, `ready`, `completed`, `cancelled`.
  - `payments.status`: `pending`, `settlement`, `capture`, `deny`, `cancel`, `expire`, `refund`.
- **Integrity**: Financial order items and prices are snapshotted at creation time. Historical line items remain intact even if catalog items are updated or deleted.

## 4. Multi-Tenant Foundation
- **Decision**: Tenant isolation is enforced via `store_id` UUID on every relational table.
- **Display IDs**:
  - Human-readable order numbers: `ORD-YYYYMMDD-XXXX` (configurable via `stores.order_prefix`).
  - Human-readable queue numbers: `A01`, `B02` (configurable via `stores.queue_prefix`).
  - Atomic sequence generation: Powered by `store_daily_sequences` with row-level locks and `ON CONFLICT DO UPDATE`.

## 5. Taxes, Service Charges, MDR, and Pricing
- **Decision**: Stored as Basis Points (`BPS`: 1% = 100 BPS) and Integer Rupiah (`BIGINT`).
- **Policy**: No universal tax or service charge is hardcoded. Store-specific PB1 and service charge rates are read from `stores` settings and snapshotted onto `orders` at checkout time.

## 6. Staff Authentication & Offline Claims
- **Decision**: 6-digit PIN with Argon2id / bcrypt hashing, lockout counters, and short-lived stateless JWT tokens containing `store_id`, `user_id`, and `role`.
- **Policy**: Offline caching does not permit confirming online dynamic QRIS payments. Payments remain pending until online gateway verification occurs.

## 7. Deployment & Backups
- **Decision**: Docker Compose on Ubuntu 24.04 LTS (Hostinger KVM 2).
- **Backups**: Daily encrypted `pg_dump` automated via `scripts/backup/backup_to_r2.sh` and uploaded to a Cloudflare R2 bucket with zero egress fees. Retention is set to 30 days.

## 8. Workspace Boundaries
- **Decision**: `myCaffe-backend` contains the Go API, PostgreSQL migrations, and Docker infrastructure manifests. The Next.js frontend is maintained in a separate repository and integrated via Docker Compose in production.
