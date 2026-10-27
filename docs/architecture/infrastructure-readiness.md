# Infrastructure Readiness & Architectural Boundaries

## 1. Hosting Tier & Commercial Compliance
- **Vercel Hobby Plan Warning**: According to Vercel Terms of Service, the Hobby plan is strictly limited to non-commercial, personal projects. A white-label QR ordering and POS system for commercial cafes **cannot** operate on the Hobby plan in production. 
  - **Resolution Path**: The frontend application repository must either use Vercel Pro (commercial license) or an alternative enterprise-compliant edge hosting solution (e.g., Cloudflare Pages / AWS Amplify / self-hosted container on VPS).

## 2. Hostinger API & MCP Boundary
- **Scope Definition**: The Hostinger API and Hostinger MCP (`https://mcp.hostinger.com`) are **strictly infrastructure management tools** (used for server health monitoring, DNS record management, and VPS maintenance operations).
- **Hard Rule**: Hostinger API/SDK must **never** be imported as an application runtime dependency inside the Go backend codebase (`mycaffe-backend`).
- **Access Control**: Any future infrastructure provisioning or DNS change via MCP requires explicit owner approval and must never be automated autonomously by agents without review.

## 3. Database Architecture & Realtime Strategy
- **Development & Staging**: Supabase PostgreSQL (`kafe-pos-dev`, project ref: `brisxdwuucvhlppcgujm`).
- **Production Recommendation**: Use a managed PostgreSQL instance (e.g., Supabase Pro or dedicated managed DB) across environments. Self-hosting PostgreSQL on a single VPS removes Supabase Realtime CDC, requires manual replication, failover, WAL archiving, and significantly increases operational maintenance overhead for a solo developer.
- **Realtime Layer**: Internal KDS and queue status updates are handled via the authenticated Go WebSocket Hub (`/ws`), while customer tracker screens consume database CDC changes.

## 4. Payment Gateway & Reconciliation Readiness
- **Midtrans Core API**: Direct API is selected for dynamic QRIS and Virtual Account payments (without Snap redirect/popup).
- **MDR & Tax Configuration**:
  - Regular QRIS MDR is 0.7% + PPN (VAT) on the transaction fee, subject to merchant agreement.
  - Store PB1 tax (restaurant tax) and service charge must remain configurable per tenant.
- **Fail-Closed Baseline**: Order creation and payment charge initiation are blocked in production until the database repository module validates catalog prices, stock availability, and persists the order before contacting Midtrans.
- **Webhook Reconciliation**: Must verify SHA512 signature, check duplicate event logs, and update payment state inside an ACID database transaction before acknowledging HTTP 200 OK to Midtrans.

## 5. Backup & Disaster Recovery
- Automated database backups must not rely solely on GitHub Actions cron triggers.
- Production backup strategy must include automated daily pg_dump / WAL archiving, retention policies (30 days), encryption at rest, and periodic restore testing drills.

