# Kafe Senja Vibe — Antigravity Starter Pack

This pack configures project guidance for Google Antigravity IDE and `agy` workflows. It does not install tools or modify your local workspace automatically.

## Contents
- `AGENTS.md`: always-on project instructions.
- `.agents/rules/`: scoped rules for Go, tenant data, payments, migrations, and release checks.
- `.agents/skills/`: reusable workflows for feature delivery, migrations, Midtrans QRIS, security review, and release readiness.
- `.agents/mcp_config.example.json`: a safe starting point for **read-only development Supabase**. Playwright is already enabled in the provided screenshot; do not register it twice.
- `docs/product/`: three product module briefs based on the requirements provided.
- `docs/architecture/decisions-before-code.md`: architecture conflicts that should be resolved before coding.
- `docs/setup/MCP-RTK-SETUP-ID.md`: Windows setup guide and MCP troubleshooting.
- `templates/frontend-AGENTS.md`: starter rules for the separate frontend repository.

## Important
- The pack is written for the current backend workspace, `myCaffe-backend`.
- It does not include secrets or live credentials.
- Merge existing files instead of blindly overwriting them.
- The Supabase MCP config is a template. Replace the development project reference and complete OAuth in Antigravity.
- RTK is installed separately using the official `rtk-ai/rtk` project.
- Do not begin payment/realtime implementation until the database/realtime architecture decision is explicit.
