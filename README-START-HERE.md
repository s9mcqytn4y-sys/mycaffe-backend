# MyCaffe POS — Antigravity Starter Pack

This pack configures project guidance for Google Antigravity IDE and `agy` workflows. It does not install tools or modify your local workspace automatically.

## Contents
- `AGENTS.md`: always-on project instructions.
- `.agents/rules/`: scoped rules for Go, tenant data, payments, migrations, and release checks.
- `.agents/skills/`: reusable workflows for feature delivery, migrations, Midtrans QRIS, security review, and release readiness.
- `deploy/docker/`: multi-stage Dockerfile and production `docker-compose.yml` for single-VPS deployment (Hostinger KVM 2).
- `deploy/nginx/`: reverse proxy configuration with WebSocket upgrades and rate limiting.
- `db/migrations/`: version-controlled PostgreSQL 18 schema migrations.
- `db/seeds/`: development seed data with neutral test entities.
- `docs/product/`: product module briefs.
- `docs/architecture/`: architectural decision records (notably ADR-002) and boundary documents.
- `templates/frontend-AGENTS.md`: starter rules for the separate frontend repository.

## Important
- The pack is written for the backend workspace: `mycaffe-backend`.
- It does not include secrets or live credentials.
- Merge existing files instead of blindly overwriting them.
