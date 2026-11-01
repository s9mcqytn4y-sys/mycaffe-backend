# Security Risk Register

Reviewed 2026-10-31 against `staging` + Phase 02.01-C. Severity: **High / Medium / Low**. Status:
**Mitigated** (control implemented and tested), **Accepted** (owner accepted the residual risk),
**Open** (needs work; backlog item named), **Blocked** (waiting on an external dependency).
Nothing here claims the system is "secure"; it lists what is controlled and what is not.

## 1. Accepted risks (owner decisions)

| ID | Risk | Why accepted | Compensating controls | Revisit when |
| ---- | ------ | -------------- | ----------------------- | -------------- |
| A1 | **6-digit PIN is a weak secret** (10⁶ space) for every role, including owner (E1) | Fast login on the floor; no password management for staff | argon2id hashing; 5 failures → 15 min lock under row lock; per-IP failure limiter; Nginx 10 r/min on `/login`; owner email on lock; generic 401; PIN policy blocks trivial PINs | Platform hosts high-value merchants, or an owner account is abused → add owner password/TOTP |
| A2 | **Targeted lockout DoS**: anyone knowing a store code + staff email can keep that account locked | Same trade-off as A1 | Owner notified by email; owner can reset PIN; 15 min window | Abuse observed → device binding or CAPTCHA on `/login` |
| A3 | **Owner can authenticate with PIN only**, so a shoulder-surfed owner PIN grants staff management and settings | E1 | Sessions max 3 + 2 h idle; every settings/staff change audited with actor; PIN-changed email | Same as A1 |
| A4 | **In-process rate limiter and workers** (single VPS, ADR-002) | Simplicity; no Redis | Nginx limits are the outer layer; workers use `SKIP LOCKED` so a second instance would not double-process | Second API instance or load balancer |
| A5 | **`log` email driver prints link secrets** in dev/demo logs | Developer convenience | Rejected at startup in production; demo stack is not used for real customers (I1) | Demo used with real staff → `.env.demo` with Gmail |
| A6 | **Staging demo exposed publicly** through a Quick Tunnel with seed data and a published dev PIN | Product demos | Unauthenticated order/payment/WS routes stay 503 in staging; Midtrans sandbox only; no real data (I1) | Real pilot data enters the laptop |
| A7 | **SMTP retry can duplicate an email** after an ambiguous timeout | SMTP has no idempotency | Newest link supersedes older ones; duplicates are harmless | — |

## 2. Residual and open risks

### High

| ID | Risk | Location | Status / next step |
| ---- | ------ | ---------- | -------------------- |
| R1 | `.env` pointed `DATABASE_URL` at a remote Supabase host | local `.env` | **Resolved 2026-10-31**: Supabase removed from `.env`, MCP config, CLI; `dev.ps1` also ignores remote DSNs. A pre-cleanup copy is in `.env.bak` (git-ignored) — delete it once local dev is confirmed |
| R2 | **No real WebSocket authentication for browsers** (headers cannot be set on `new WebSocket`) | `internal/handler/order_handler.go` `HandleWebSocket` | Mitigated by containment: `/ws` is 503 in staging/production; Phase 03 one-time WS ticket |
| R3 | **No backup restore drill yet** (scripts exist, never restored on a real VPS) | `scripts/backup/*` | Blocked — needs VPS; drill is a release gate |

### Medium

| ID | Risk | Location | Status / next step |
| ---- | ------ | ---------- | -------------------- |
| R4 | **Split-bill settlement marks all splits paid** | `internal/service/payment_service.go` `commitPaidOrderTx` | Open — must be reworked before open tab/split (backlog 8); POS currently creates single-split orders |
| R5 | **Duplicate settlement only emits an event**; nobody is alerted yet | `flagDuplicateSettlementTx` | Open — owner email + refund queue UI (backlog 8, 11) |
| R6 | Sentry captures startup errors only; **HTTP panics are not reported** (gin Recovery logs them) | `cmd/api/main.go` | Open — add Sentry middleware (module `sentry-go/gin`) with PII scrubbing |
| R7 | **No metrics/tracing** (latency, error rate, worker lag, email backlog) | — | Open — Prometheus-style `/metrics` on an internal port or Sentry performance (backlog) |
| R8 | **Media upload not implemented**; R2 keys exist only as config | `internal/config` | Open — K9 design: presigned PUT, magic-byte check, EXIF strip, WebP variants |
| R9 | **Email failures are invisible** to the owner (`email_outbox.status='failed'`) | `email_outbox` | Open — backoffice view + alert (backlog 11) |
| R10 | Production Nginx has no `client_max_body_size` override (nginx default 1 MB) and no CSP | `deploy/nginx/nginx.prod.conf` | Accepted for API-only host; add CSP on the frontend host |
| R11 | **Guest ordering not authenticated yet** | `POST /api/v1/orders` | Mitigated by containment (503 in staging/production); 02.01-D/E |

### Low

| ID | Risk | Location | Status |
| ---- | ------ | ---------- | -------- |
| R12 | Fixed WIB timezone in email copy | `service/email_service.go` `formatLocalTime` | Accepted (shortcut comment); use store timezone for non-WIB stores |
| R13 | Login timing differs slightly between "unknown store" (no DB tx) and "unknown user" | `AuthService.Login` | Accepted; argon2 cost dominates |
| R14 | `govulncheck` reports 1 vulnerability in a required module, not called by our code | `go.mod` | Monitored in CI (`build-and-test`) |

## 3. Mitigated in Phases 01–02 (evidence)

| Control | Evidence |
| --------- | ---------- |
| Tenant isolation: RLS on every tenant table, non-owner `mycaffe_app NOBYPASSRLS`, `WithTenantTx` | `scripts/verify/verify_schema_constraints.sql`, cross-tenant integration tests |
| Narrow `SECURITY DEFINER` allow-list (4 functions, `REVOKE … FROM PUBLIC`) | AGENTS.md; verify script checks PUBLIC execute |
| Store and actor taken only from the token on staff routes | `TestRBACRoutesIntegration` (body `store_id` ignored, payment `verified_by` = token user) |
| Permission checked before the handler runs | `RequirePermission` (bug found and fixed by the integration test) |
| Refresh rotation, reuse detection, tab-race grace, session cap, idle timeout | `TestAuthServiceIntegration_EndToEnd` |
| Single-use, hash-only account links; no enumeration on forgot-PIN; per-user cooldown | `TestStaffLifecycleIntegration` |
| Webhook: SHA-512 signature, amount/currency/status validation, durable inbox, duplicate settlement detection | payment integration tests |
| Client IP not spoofable (`TRUSTED_PROXIES`), header timeouts, 1 MiB body cap, request ID | `cmd/api/main.go`, `TestRequestIDAndBodyLimit` |
| SMTP: STARTTLS mandatory on 587, header-injection-safe MIME | `internal/pkg/mailer` tests |
| No secrets in git; prod compose fails without required secrets | `.env.example` placeholders, `${VAR:?}` |

## 4. Environment, network & infrastructure review

| Area | Dev (laptop) | Demo (Quick Tunnel) | Production (Hostinger KVM 2, blocked) |
| ------ | -------------- | --------------------- | ---------------------------------------- |
| Inbound ports | none (all `127.0.0.1`) | none (outbound tunnel) | 80/443 only (Nginx); DB and API internal |
| TLS | n/a | Cloudflare edge | Let's Encrypt via bootstrap entrypoint; HSTS |
| Proxy trust | none | edge subnet `172.29.10.0/24` | edge subnet `172.30.0.0/24` |
| Rate limits | — | Nginx + app limiter, per real visitor IP | Nginx + app limiter |
| WebSocket path | `/ws` header auth (dev only) | 503 | 503 until Phase 03 ticket |
| Email | `log` | `log` or Gmail (`.env.demo`) | Hostinger SMTP 465, SPF/DKIM/DMARC |
| Storage | — | — | R2 (planned, K9) + encrypted DB backups to R2 |
| Observability | stdout logs | `docker compose logs` | stdout + Sentry (startup); metrics planned |
| Firewall | Windows Firewall all profiles on (verified) | same | VPS: allow 22 (key-only), 80, 443; deny rest (to configure at provisioning) |

## 5. Module dependency review (`go.mod`)

| Dependency | Purpose | Note |
| ------------ | --------- | ------ |
| gin v1.12 | HTTP | trusted proxies configured |
| pgx v5.11 + sqlx | PostgreSQL | parameterized queries only |
| golang-jwt v5.3 | staff access tokens | HS256 only, `kid`/`typ` enforced |
| x/crypto (argon2) | PIN hashing | promoted to direct |
| x/term | hidden PIN prompt in CLI | already in graph |
| gorilla/websocket | realtime hub | origin check enforced |
| midtrans-go | Core API | sandbox in dev/demo |
| sentry-go | error reporting | HTTP middleware missing (R6) |
| godotenv | local `.env` | never overrides set variables |

No CGO dependencies; static binaries. Run `./scripts/dev.ps1 check` (includes `govulncheck`) before each PR.
