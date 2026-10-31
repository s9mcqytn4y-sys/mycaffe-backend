# MyCaffe Engineering plugin

This repository-local Claude Code plugin provides task-specific workflows for the MyCaffe Go/PostgreSQL backend.

## Skills

- phase02-auth-rbac: inspect and design staff authentication, session lifecycle, and the four-role permission model.
- signed-qr-table-session: design and review printed table QR credentials, guest table sessions, revocation, and guest-order isolation.
- postgres-rls-migrations: plan and review additive PostgreSQL 18 migrations, composite tenant constraints, RLS, rollback, and integration tests.
- tenant-security-review: review tenant boundaries, trust inputs, IDOR risks, token handling, and auditability.

## Install from the repository root

1. Start Claude Code in the repository.
2. Add the local marketplace: /plugin marketplace add .
3. Install the project plugin: /plugin install mycaffe-engineering@mycaffe-tools --scope project.
4. If prompted, approve the local marketplace and plugin after reviewing the source.
5. Run /reload-plugins if Claude Code asks for it.

The plugin is versioned in this repository. Bump the plugin and marketplace versions when publishing a new release.

## Safety

These skills are review and planning aids, not security enforcement. Repository rules, server-side authorization, PostgreSQL RLS, tests, and human approval remain authoritative. The plugin must not connect to production databases or deploy production infrastructure.
