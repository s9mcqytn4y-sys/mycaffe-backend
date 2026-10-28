# ADR-002: Single-VPS Modular Monolith Deployment Topology (Docker Compose & PostgreSQL 18)

## Status
**ACCEPTED** (Supersedes Supabase runtime dependency; approves Hostinger KVM 2 target architecture)

## Context
Initial design iterations leveraged Supabase for database hosting, authentication, and realtime subscriptions. However, for a white-label multi-tenant POS platform intended for SME café owners:
1. **Cost & Vendor Lock-in**: PaaS platforms introduce egress fees, connection limits, and per-project tiers that scale poorly across independent merchant deployments.
2. **Data Sovereignty & Privacy**: Café transaction histories, financial ledgers, and staff PINs should remain completely isolated within the operator's owned or dedicated infrastructure.
3. **Hardware Efficiency**: Modern SME POS, KDS, and QR ordering traffic (e.g. 100-500 orders/day per café) can be effortlessly handled on a single affordable VPS (such as Hostinger KVM 2: 2 vCPU, 8 GB RAM, 100 GB NVMe SSD) costing ~\$6–\$8/month.

## Decision
We adopt a **Single-VPS Modular Monolith Architecture** managed via **Docker Compose**:

```
+---------------------------------------------------------------------------------+
|                               Hostinger KVM 2 VPS                               |
|                                                                                 |
|   +-------------------------------------------------------------------------+   |
|   |                  Nginx Reverse Proxy (Ports 80 / 443)                   |   |
|   |  - TLS / Let's Encrypt Certbot                                          |   |
|   |  - API Rate Limiting (30 rps general, 10 rps webhooks)                  |   |
|   |  - WebSocket Upgrade (/ws)                                              |   |
|   |  - Gzip Compression & Strict Security Headers                           |   |
|   +-------------------+---------------------------------+-------------------+   |
|                       |                                 |                       |
|         /api/*, /ws   |                                 | /*                    |
|                       v                                 v                       |
|   +------------------------------------+   +--------------------------------+   |
|   |   MyCaffe Go API Backend (8080)    |   |  Next.js Standalone PWA (3000) |   |
|   |  - Gin Web Framework               |   |  - Customer QR Ordering        |   |
|   |  - In-Memory WebSockets (KDS/TV)   |   |  - Staff POS & Cashier UI      |   |
|   |  - Server-side Pricing & Totals    |   |  - Kitchen Display System      |   |
|   |  - Midtrans Webhook Handler        |   |  - TV Queue Display            |   |
|   |  - sqlx + pgx Database Pool        |   +--------------------------------+   |
|   +-------------------+----------------+                                        |
|                       | Internal Network                                        |
|                       v                                                         |
|   +-------------------------------------------------------------------------+   |
|   |                    PostgreSQL 18 Container (:5432)                      |   |
|   |  - Shared Buffers: 1GB | Effective Cache: 3GB | Work Mem: 16MB          |   |
|   |  - Persistent Named Volume (pgdata)                                     |   |
|   |  - Foreign Key RESTRICT on Financial History                            |   |
|   |  - Atomic Sequences for Daily Queue & Order Numbers                     |   |
|   +------------------------------------+------------------------------------+   |
|                                        |                                        |
+----------------------------------------|----------------------------------------+
                                         | Nightly Encrypted Backup (pg_dump + AES-256)
                                         v
                         +-------------------------------+
                         |   Cloudflare R2 Bucket        |
                         |   - Zero Egress Cost          |
                         |   - Encrypted Disaster Backup |
                         +-------------------------------+
```

### Resource Allocation Budget (8 GB RAM Total)
- **PostgreSQL 18**: Max limit `3.5 GB` RAM (shared buffers: 1 GB, cache: 3 GB).
- **Go API Backend**: Max limit `1.0 GB` RAM (typically idles at 30–80 MB).
- **Next.js Standalone Frontend**: Max limit `1.5 GB` RAM (typically idles at 150–300 MB).
- **Nginx Reverse Proxy**: Max limit `256 MB` RAM.
- **Operating System & Kernel Buffer Cache**: ~`1.7 GB` RAM reserved.

### Cloudflare R2 Policy
Cloudflare R2 is **not required** for local asset serving (product images are served by Nginx from local persistent volumes or public CDNs). R2 is retained **exclusively as a zero-egress offsite disaster recovery destination** for encrypted database backups generated via `scripts/backup/backup_to_r2.sh`.

## Consequences
- **Positive**:
  - Full operational independence: No dependency on external database PaaS, connection pooling proxies, or paid tiers.
  - Reproducible deployments: `docker compose up -d` boots the entire stack identically in local dev, staging, or production.
  - Low latency: Inter-service communication between Go API and PostgreSQL occurs over internal Docker bridge networks with sub-millisecond roundtrips.
- **Negative / Operational Responsibilities**:
  - The operator manages container restarts, volume backups, and host OS security patches.
  - Automated offsite backup verification must be tested regularly.

