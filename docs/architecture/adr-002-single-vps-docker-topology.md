# ADR-002: Single-VPS Modular Monolith Deployment Topology (Docker Compose & PostgreSQL 18)

## Status
**ACCEPTED** (Supersedes external database PaaS runtime dependencies; defines Hostinger KVM 2 target architecture)

## Context
For a white-label multi-tenant POS platform intended for SME café and restaurant operators:
1. **Cost & Vendor Lock-in**: Managed database PaaS platforms introduce egress fees, connection limits, and per-project tiers that scale poorly across independent merchant deployments.
2. **Data Sovereignty & Privacy**: Café transaction histories, financial ledgers, and staff PIN hashes remain isolated within the operator's dedicated infrastructure.
3. **Hardware Efficiency**: Modern SME POS, KDS, and QR ordering traffic (100-500 orders/day per store) runs with low latency on a single VPS (Hostinger KVM 2: 2 vCPU, 8 GB RAM, 100 GB NVMe SSD).

## Decision
We adopt a **Single-VPS Modular Monolith Architecture** managed via **Docker Compose**, separating Local/CI (`deploy/docker/docker-compose.yml`) from Production (`deploy/docker/docker-compose.prod.yml`):

```
+---------------------------------------------------------------------------------+
|                         Hostinger KVM 2 VPS (Ubuntu 24.04)                      |
|                                                                                 |
|   +-------------------------------------------------------------------------+   |
|   |             Nginx Reverse Proxy (Ports 80 -> 443 TLSv1.2/1.3)           |   |
|   |  - Let's Encrypt Certbot Webroot Renewal                                |   |
|   |  - API Rate Limiting (30 rps general, 10 rps webhooks)                  |   |
|   |  - Direct HTTPS Webhook Proxy (/api/v1/payments/midtrans/webhook)       |   |
|   |  - WebSocket Upgrade (/ws) & Gzip Compression                           |   |
|   +-------------------+---------------------------------+-------------------+   |
|                       |                                 |                       |
|         /api/*, /ws   |                                 | /*                    |
|                       v                                 v                       |
|   +------------------------------------+   +--------------------------------+   |
|   |   MyCaffe Go API Backend (:8080)   |   |  Next.js Standalone PWA (:3000)|   |
|   |  - Immutable GHCR Image            |   |  - Immutable GHCR Image        |   |
|   |  - Midtrans Core API Custom UI     |   |  - Customer QR Ordering        |   |
|   |  - Server-side Pricing & Split Bill|   |  - Staff POS & Cashier UI      |   |
|   |  - Role: mycaffe_app (NOBYPASSRLS) |   |  - Kitchen Display System      |   |
|   +-------------------+----------------+   +--------------------------------+   |
|                       | Internal Docker Bridge Network                          |
|                       v                                                         |
|   +-------------------------------------------------------------------------+   |
|   |                    PostgreSQL 18 Container (:5432)                      |   |
|   |  - Official PG18 Volume Mount: pgdata:/var/lib/postgresql               |   |
|   |  - Dual Roles: mycaffe_migrator (DDL) & mycaffe_app (DML + RLS)         |   |
|   |  - Native Row-Level Security (USING store_id = current_store_id())      |   |
|   |  - Foreign Key RESTRICT on Financial History (orders & payments)        |   |
|   +------------------------------------+------------------------------------+   |
|                                        |                                        |
+----------------------------------------|----------------------------------------+
                                         |
            +----------------------------+----------------------------+
            | Public Product Media (CDN)                              | Nightly GPG AES256+SHA512 Backup
            v                                                         v
+-------------------------------+                         +-------------------------------+
| Cloudflare R2: mycaffe-media  |                         | Cloudflare R2: mycaffe-backups|
| - Public/CDN Product Images   |                         | - Private Zero-Egress Bucket  |
| - Scoped Media API Credentials|                         | - GPG + .sha256 Manifest      |
+-------------------------------+                         +-------------------------------+
```

### 1. Environment & Compose Separation
- **Local Dev & CI (`deploy/docker/docker-compose.yml` + `deploy/nginx/nginx.conf`)**:
  - Builds the Go API and Migrator from local source (`deploy/docker/Dockerfile`), exposes HTTP `:80`, and mounts `pgdata:/var/lib/postgresql` per the official PostgreSQL 18 image specification.
- **Production (`deploy/docker/docker-compose.prod.yml` + `deploy/nginx/nginx.prod.conf`)**:
  - Pulls immutable, version-tagged container images (`ghcr.io/s9mcqytn4y-sys/mycaffe-api:${API_IMAGE_TAG}` and `ghcr.io/s9mcqytn4y-sys/mycaffe-web:${WEB_IMAGE_TAG}`). Source code is never compiled on the production VPS.
  - Runs the one-shot `migrate` service (`/app/mycaffe-migrate -action=up`) under `DATABASE_MIGRATION_URL` (`mycaffe_migrator` owner role) with `pg_advisory_lock` before starting `api`.
  - Runs the `api` service under `DATABASE_URL` (`mycaffe_app` role with `NOBYPASSRLS`).
  - Enforces HTTPS on `:443` (`ssl_protocols TLSv1.2 TLSv1.3; http2 on;`) and routes `/api/v1/payments/midtrans/webhook` directly to `api:8080` without HTTP 301/302 redirects.

### 2. Resource Allocation Budget (8 GB RAM Total)
- **PostgreSQL 18**: Max limit `3.5 GB` RAM (`shared_buffers=1GB`, `effective_cache_size=3GB`).
- **Go API Backend**: Max limit `1.0 GB` RAM.
- **Next.js Standalone Frontend**: Max limit `1.5 GB` RAM.
- **Nginx Reverse Proxy**: Max limit `256 MB` RAM.
- **Operating System & Kernel Buffer Cache**: ~`1.7 GB` RAM reserved.

### 3. Cloudflare R2 Dual-Bucket Policy
Cloudflare R2 is used with two strictly separated buckets and separate IAM API tokens:
1. **`mycaffe-media` (`R2_BUCKET_NAME`)**: Stores public product catalog and store branding media served via Cloudflare CDN (`R2_PUBLIC_URL`), keeping the VPS disk lean.
2. **`mycaffe-backups` (`R2_BACKUP_BUCKET`)**: Private bucket used exclusively by `scripts/backup/backup_to_r2.sh` for nightly `pg_dump` archives encrypted with GPG (`--cipher-algo AES256 --digest-algo SHA512`) and accompanied by a `.sha256` integrity manifest.

## Consequences
- **Positive**:
  - Full operational independence with zero external database PaaS dependency.
  - Defense-in-depth multi-tenant isolation combining Go query scoping and PostgreSQL 18 Row-Level Security (`NOBYPASSRLS`).
  - Reproducible, verifiable deployments with automated migration tracking (`schema_migrations`).
- **Operational Responsibilities**:
  - Host OS patches, TLS renewal via the `certbot` container, and periodic restore drills from `mycaffe-backups` must be maintained on schedule.
