# Audit Backlog (v0.5.0 audit)

Tickets from the v0.5.0 production-readiness audit (2026-11-02). The decision register, roadmap and PR
titles refer to these IDs. Status reviewed 2026-11-04 against `staging` after v0.6.0.

**Owner direction (2026-11-03):** large new features are **frozen until go-live**: ADR-005 offline cash
sync, inventory (C5), promotions/loyalty and delivery channels. Rows marked *Frozen* are not started
without explicit owner approval. The go-live work is tracked as PR A and PR B in [Roadmap.md](../Architecture/Roadmap.md).

Status: *Done* (merged to `staging`, tested in CI), *Planned*, *Frozen*, *To verify* (partly in code,
gap not yet confirmed).

## P0: required before the first client goes live

| ID | Title | Status | Evidence / next step |
|----|-------|--------|----------------------|
| P0-01 | Run every integration test in CI | Done | PR #38 |
| P0-02 | Scheduled backups, retention tiers, restore drill | Done (drill on a real VPS pending) | PR #39, [BackupAndRestore.md](../Setup/BackupAndRestore.md) |
| P0-03 | Structured logging and metrics | Done | PR #40 |
| P0-04 | Alerting and external uptime | Done (Grafana Cloud account pending, owner) | PR #41, [Monitoring.md](../Setup/Monitoring.md) |
| P0-05 | Deploy by digest, idempotent deploy/rollback script | Done (first VPS run pending) | PR #42, [Deploy.md](../Setup/Deploy.md) |
| P0-06 | Supply chain: provenance, SBOM, image scan | Done (tag/Release path runs on the next `v*` tag) | PR #43 |
| P0-07 | Real client IP and VPS firewall (IN1 Named Tunnel) | Planned | Deployment change, needs owner approval |
| P0-08 | Outbox dead-letter and purge of technical tables | Planned | `attempts`, `last_error`, `dead_at`; purge `outbox_events`, `ws_tickets`, `guest_sessions`, `payment_notification_logs` |
| P0-09 | Provisioning and incident runbook per client | Planned | Go-live gate |
| P0-10 | ADR-005 offline cash sync | **Frozen** | Owner freeze 2026-11-03 |
| P0-11 | Receipts (ESC/POS + digital) | Done (printer QR untested on real hardware) | PR B1, migration 000020 |
| P0-12 | Refresh risk register and release notes | Done | Risk register refreshed 2026-11-04; release notes on GitHub Releases |

## P1: first weeks after go-live

| ID | Title | Status | Evidence / next step |
|----|-------|--------|----------------------|
| P1-01 | RF1 non-cash refunds | In progress | RF1-c, RF1-e done (B4, migration 000023); RF1-a/b/d/f after sandbox test |
| P1-02 | Daily sales report | Done | PR B2, migration 000021; calendar business day until PD5 (04:00 cutoff) is approved |
| P1-03 | Owner notifications (void/refund/cash variance/lockout) | Planned | Lockout warning starts in PR A F1 |
| P1-04 | Remote approval (single-use `approval_requests`) | Planned | — |
| P1-05 | Emergency staff revoke | To verify | `staff_admin.go` deactivates staff; WebSocket disconnect added in PR A |
| P1-06 | Catalog price-change audit | To verify | Modifier price changes are audited (`catalog_modifier.go`); confirm variant prices |
| P1-07 | ADR-006 stock ledger | **Frozen** | Inventory |
| P1-08 | Ingredients, BOM, COGS snapshot | **Frozen** | Inventory |
| P1-09 | Suppliers, PO, goods receipt | **Frozen** | Inventory |
| P1-10 | Blind stock count and waste | **Frozen** | Inventory |
| P1-11 | KDS operations report (p50/p90 per station/hour) | Planned | After B2 |
| P1-12 | Integration test for Q7 late payment | Planned | `kds_integration_test.go` |

## P2: later

| ID | Title | Status |
|----|-------|--------|
| P2-01 | Operating expenses + approval | Planned |
| P2-02 | Settlement import and matching (CSV first) | Planned |
| P2-03 | Management P&L and cash flow | Planned (depends on P1-08, frozen) |
| P2-04 | Journal export (IN4 neutral CSV) | Planned |
| P2-05 | Tax summary per period (no compliance claim) | Planned |
| P2-06 | Reorder recommendation | **Frozen** (inventory) |
| P2-07 | k6 load test and capacity baseline (p95 < 300 ms) | Planned |
| P2-08 | WAL archiving (RPO < 15 min, only if a contract needs it) | Planned |

## P3: future

P3-01 attendance & schedules, P3-03 multi-outlet (Model B), P3-05 email receipts & marketing consent (E4).
Not scheduled.
