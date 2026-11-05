# Architecture & Executive Decisions (`memory/DECISIONS.md`)

## ADR & Executive Decisions Summary
1. **ADR-002 (Single-VPS Docker Modular Monolith)**:
   - Production target is a single Hostinger KVM 2 VPS running Docker Compose (`postgres:18-alpine`, Go API, Next.js PWA, Nginx).
   - Demo environment runs on the Director's Windows 11 workstation exposed via Cloudflare Named Tunnel `mycaffe-demo` (`https://demo.abbdullaziz.online`).
2. **Payment Gateway Policy (No Midtrans Snap)**:
   - Strictly use Midtrans Core API (`/v2/charge`) in a 2-phase database transaction.
   - `payments.id.String()` is used as both `provider_order_id` and Midtrans `Idempotency-Key`.
   - Demo & development environments use Midtrans Sandbox (`MIDTRANS_IS_PRODUCTION=false`).
3. **Media & Backup Storage (Cloudflare R2 Dual-Bucket)**:
   - Public catalog/store media: `mycaffe-media` served via custom domain `https://cdn.abbdullaziz.online` (`1:1` and `4:3` `.webp` for menu items, `1:1` for variants/addons/logos, `16:9` for onboarding banners).
   - Private encrypted DB backups: `mycaffe-backups` (GPG AES256+SHA512).
4. **Seeder Policy (`db/seeds/development.sql`)**:
   - Zero pre-seeded transactions (`orders`, `order_items`, `payments`, `store_daily_sequences` must be empty on fresh seed).
   - Preserve core entity UUIDs (`11111111-...`, `22222222-...`, `a0000001-...` to `a0000005-...`, `d0000001-...` to `d0000006-...`, `10000001-...` to `10000003-...`, `20000001-...` to `20000006-...`) so integration test suites remain deterministic.
   - Owner email (`abdulaziz543333@gmail.com`) is preserved on `a0000001-0000-4000-8000-000000000001`.
5. **Agent Context Budget Policy (3-Tier Skill Curation)**:
   - Consolidate multi-disciplinary workflows into 6 auto-invoked master skills (`vibe-coder-pro`, `blender-roblox-pipeline`, `human-writer-content`, `bizdev-client-hunter`, `netops-cloudflare-docker`, `claude-artifact-builder`).
   - Keep heavy standalone frameworks/apps indexed in `memory/TOOLCHAIN_CATALOG.md` rather than injecting all 48 repos into every session's system prompt.

