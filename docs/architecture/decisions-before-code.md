# Architecture Decisions & Resolution Log

This document records architectural decision gates and their approved resolutions.

## 1. Production Database vs Realtime
- **Decision**: **Option B: Native PostgreSQL 18 with In-Memory Go WebSocket Hub and Transactional Outbox** (Resolved in [ADR-002](adr-002-single-vps-docker-topology.md)).
- **Rationale**: A single Hostinger KVM 2 VPS (2 vCPU, 8 GB RAM, 100 GB SSD) runs PostgreSQL 18 and the Go backend inside Docker Compose. Real-time updates for KDS, POS, and TV queue display are recorded atomically in `outbox_events` and delivered via the authenticated Go WebSocket Hub (`internal/realtime`), scoped strictly by tenant `store_id`. This eliminates external database PaaS dependencies and recurring cloud subscription costs.

## 2. Static QRIS vs Dynamic QRIS
- **Decision**: **Dynamic Gateway QRIS via Midtrans Core API** is the primary automated payment flow.
- **Rules**: Static store QRIS is treated strictly as an offline/manual cashier-reconciliation method (`static_qris`); automated order status transitions are prohibited for static QRIS unless verified by cashier action (`verified_by_user_id`). Dynamic QRIS initiates a dedicated transaction linked to `provider_order_id` (`{order_number}-S{split}-A{attempt}`) and transitions state upon verified gateway notification.

## 3. Payment State vs Preparation State
- **Decision**: Modeled as independent state machines in the database schema and Go domain layer (aligned 1:1 with PostgreSQL `CHECK` constraints):
  - `orders.status` (`OrderStatus`): `pending_payment`, `queued`, `preparing`, `ready`, `served`, `completed`, `cancelled`.
  - `orders.payment_status` (`OrderPaymentStatus`): `unpaid`, `pending`, `partially_paid`, `paid`, `failed`, `cancelled`, `expired`, `partially_refunded`, `refunded`, `chargeback`, `partial_chargeback`.
  - `payments.status` (`PaymentAttemptStatus`): `pending`, `authorized`, `paid`, `failed`, `cancelled`, `expired`, `partially_refunded`, `refunded`, `chargeback`, `partial_chargeback`.
  - `payment_splits.status` (`SplitBillStatus`): `unpaid`, `pending`, `partially_paid`, `paid`, `cancelled`, `refunded`.
- **Integrity**: Financial order items, modifier options, and 1-level sub-addons are snapshotted at creation time into `order_items` and `order_item_options`. Historical line items remain immutable even if catalog items are updated or deleted.

## 4. Multi-Tenant Foundation & Display IDs
- **Decision**: Tenant isolation is enforced via `store_id` UUID on every relational table combined with PostgreSQL 18 Row-Level Security (`ENABLE ROW LEVEL SECURITY`) under `mycaffe_app` (`NOSUPERUSER NOBYPASSRLS`).
- **Display IDs**:
  - Human-readable order numbers: `{ORDER_PREFIX}-{YYYYMMDD}-{XXXX}` (e.g., `ORD-20261029-0001`, configurable via `stores.order_prefix`).
  - Human-readable queue numbers: `{QUEUE_PREFIX}{XXX}` (e.g., `A001`, `B001`, configurable via `stores.queue_prefix`).
  - Atomic sequence generation: Powered by `store_daily_sequences` per store local business date (`stores.timezone`) with `ON CONFLICT (store_id, business_date) DO UPDATE`.

## 5. Taxes, Service Charges, MDR, and Pricing
- **Decision**: Stored as Basis Points (`BPS`: 1% = 100 BPS) and whole integer Indonesian Rupiah (`BIGINT` / `int64`) with deterministic *Round Half Up* arithmetic.
- **Policy**: No universal tax or service charge is hardcoded. Store-specific PB1 (`tax_bps`) and service charge (`service_bps`) rates are read from `stores` settings inside `WithTenantTx` and snapshotted onto `orders` at checkout time.

## 6. Staff Authentication, Roles & Shift Flexibility
- **Decision**: Four streamlined operational roles (`owner`, `cashier`, `barista`, `kitchen`) without a separate `manager` role or multi-step approval gates that block cafe throughput. Sensitive cashier actions require a PIN and mandatory reason logged to append-only `audit_logs`.
- **Shift Modes**: Configurable per store via `stores.shift_mode` (`single_auto` for single-shift cafes or `multi_shift` for multi-shift cash drawer reconciliation).

## 7. Deployment, Backups & Zero In-Place Coding
- **Decision**: Docker Compose (`deploy/docker/docker-compose.prod.yml`) on Ubuntu 24.04 LTS (Hostinger KVM 2) pulling immutable images from `ghcr.io`.
- **Zero In-Place Coding**: Editing source code or compiling binaries directly on the production VPS is strictly prohibited.
- **Backups**: Nightly `pg_dump` encrypted with GPG (`AES256` + `SHA512`) and `.sha256` manifest uploaded to private Cloudflare R2 bucket `mycaffe-backups` via `scripts/backup/backup_to_r2.sh` and verified via `scripts/backup/restore_from_r2.sh`.

## 8. Workspace Boundaries
- **Decision**: `mycaffe-backend` contains the Go API, PostgreSQL 18 migrations, and Docker/Nginx infrastructure manifests. The Next.js frontend is maintained in a separate repository and integrated via Docker Compose in production.
