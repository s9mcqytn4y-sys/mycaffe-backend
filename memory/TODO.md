# MyCaffe Backend — Action Queue (`memory/TODO.md`)

## Active Sprint (`feat/seeder-catalog-webp-agent-setup`)
- [x] Merge PR #65 (`feat/infra-cloudflare-sentry-clean-code`) to `staging`.
- [x] Fix Windows 11 `PATH` priority (`Python 3.12`, `uv`, `Blender 5.2.3 LTS`, `agy`, `wrangler`, `defuddle`).
- [x] Establish Global (`~/.claude/CLAUDE.md`) & Project (`./CLAUDE.md`, `./memory/*`, `.claude/settings.json`, `.mcp.json`) Agent Architecture.
- [x] Complete 6 Core Auto-Invoked Skills (`vibe-coder-pro`, `blender-roblox-pipeline`, `human-writer-content`, `bizdev-client-hunter`, `netops-cloudflare-docker`, `claude-artifact-builder`) + Tier-2 curated skill sync.
- [x] Generate `.webp` catalog, variant, addon, logo, and onboarding image assets and upload to Cloudflare R2 (`https://cdn.abbdullaziz.online/seeds/...`).
- [x] Overhaul `db/seeds/development.sql` (0 transactions, realistic catalog & store onboarding data) and self-seed legacy test fixture in `internal/service/order_service_integration_test.go`.
- [x] Run `gofmt`, `go vet ./...`, `go test -v -race ./...`, double-run `cmd/migrate -action=seed`, push branch, and open PR to `staging`.

## Upcoming Backlog
- [ ] Promote `staging` -> `main` once client VPS is provisioned.
- [ ] Switch `MIDTRANS_IS_PRODUCTION=true` after Midtrans Business Review approval completes.
