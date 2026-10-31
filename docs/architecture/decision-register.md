# Decision Register

Owner decisions that bind implementation. Recorded 2026-10-31 (structured interview with the owner).
Earlier decisions live in [decisions-before-code.md](decisions-before-code.md) and ADR-003
(PR #7, `docs/architecture/adr-003-staff-authentication-session-and-rbac.md`).

## Approved

| ID | Decision | Approved option | Implementation notes | Blocks |
|----|----------|-----------------|----------------------|--------|
| D1 | Staff login identity | `store_slug + email + PIN` | ADR-003 D1 | 02.01-B |
| D2 | PIN policy | 6–12 digits, weak-PIN denylist, argon2id `m=19456,t=2,p=1` | ADR-003 D2 | 02.01-B |
| D3 | Throttling & lockout | 5 consecutive failures → locked 15 min; generic `401` | ADR-003 D3 | 02.01-B |
| D4 | Token TTL | Access 15 min, refresh 12 h absolute, rotation + reuse detection | ADR-003 D4 | 02.01-B |
| O1 | Void / refund | Cashier re-enters **own PIN** + mandatory reason. Above a per-store amount threshold (default **Rp100.000**) the **owner PIN** is required. All attempts go to `audit_logs`. | New store setting `void_refund_owner_threshold` (int64 IDR). Supersedes ADR-003 D6 "out of scope" note on thresholds. | 02.01-C, POS void/refund |
| O2 | Production domains | POS frontend and API share **one registrable domain** (e.g. `pos.<domain>` + `api.<domain>`) | Refresh token in `__Secure-mc_staff_rt` HttpOnly cookie, `SameSite=Strict` (ADR-003). Domain name itself still to be purchased/assigned. | 02.01-B refresh transport |
| O3 | Staff sessions | Max **3** active sessions per staff (oldest revoked), **idle timeout 2 h**, absolute **12 h** | `STAFF_MAX_SESSIONS_PER_USER=3`, new `STAFF_SESSION_IDLE_TIMEOUT=2h` checked on refresh | 02.01-B |
| O4 | Discount & stock override | Cashier may discount up to a per-store max percent (default **10%**) with mandatory reason. Above that, and **every** stock override, require owner PIN. | New store setting `cashier_max_discount_bps` (default 1000). | POS discounts, inventory |
| Q5 | Printed table QR lifetime | **No time expiry.** Version-based revocation (`qr_version`) per table, signing-key rotation via key ID, plus a per-store **emergency pause** that disables all QR session creation instantly. | Dedicated QR signing key (never `JWT_SECRET_KEY`). | 02.01-D |
| Q6 | Guest sessions | **One session per device**; a guest sees only orders bound to its own session. TTL **3 h**, or ends when all of its orders are paid and completed. | Distinct `aud`/`typ` from staff tokens. | 02.01-E |
| Q7 | Late settlement after expiry | Keep current behavior: revive the order to the queue if stock allows, otherwise cancel + `payment.late_settlement_stock_exhausted`. **Add a "late payment" flag** visible on KDS and cashier screens. | Flag not implemented yet. | KDS (Phase 03) |
| Q8 | Demo / staging environment | Local Docker Compose stack + **Cloudflare Quick Tunnel**, auto-restart, `APP_ENV=staging`, Midtrans sandbox | [docs/setup/demo-staging-environment.md](../setup/demo-staging-environment.md) | — |

## Resolved from existing sources (not re-asked)

| Topic | Resolution | Source |
|-------|-----------|--------|
| Split bill semantics | Deferred until the single-payer path is stable; equal vs item split, rounding and partial refunds to be specified then. Code currently assumes **one split per order** (`commitPaidOrderTx` settles all splits). | `docs/product/module-1-customer-ordering.md` |
| Roles | Exactly `owner`, `cashier`, `barista`, `kitchen`; no new roles. | `AGENTS.md`, migration `000001` |

## Still open

| Topic | Needed for | Default until decided |
|-------|-----------|-----------------------|
| Production domain name & DNS | Named Cloudflare Tunnel / TLS, cookie domain (O2) | Quick Tunnel URL for demo only |
| Midtrans production credentials & VPS provisioning | Production cutover | Sandbox only |
| WebSocket ticket lifecycle (TTL, single use) | Phase 03 realtime | `/ws` stays `503` in staging/production |
