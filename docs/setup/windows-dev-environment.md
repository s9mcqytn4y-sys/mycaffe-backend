# Windows 11 Laptop: Environments, Networking & Developer Workflow

Until a production VPS and domain exist, this laptop runs **development and demo only** — no real
cafe transactions (owner decision I1). Everything is driven by `scripts/dev.ps1` (decision I4).

## 1. Environments

| Environment | How it runs | `APP_ENV` | Database | Reachable from | Purpose |
| ------------- | ------------- | ----------- | ---------- | ---------------- | --------- |
| **dev** | `go run ./cmd/api` on Windows + PostgreSQL 18 in Docker | `development` | `mycaffe-postgres-dev` (`127.0.0.1:5432`), seed data | this laptop only | daily coding, debugging (VS Code F5), full order/payment simulation |
| **test** | `dev.ps1 test-int`: throwaway PostgreSQL 18 container | `test` | `mycaffe-pg-integration` (`127.0.0.1:55432`), created and deleted per run | this laptop only | integration tests identical to CI |
| **demo / staging** | `dev.ps1 demo-up`: whole stack in Docker + Cloudflare Quick Tunnel | `staging` | `mycaffe-demo` volume, seed data | anyone with the `*.trycloudflare.com` URL (HTTPS) | showing the product, testing on tablets/phones, Midtrans sandbox webhooks |
| **production** | `deploy/docker/docker-compose.prod.yml` on Hostinger KVM 2 | `production` | VPS PostgreSQL | public domain | **blocked**: no VPS/domain yet |

Run **one stack at a time** on 8 GB RAM: `dev.ps1 down` before `demo-up` and vice versa.

## 2. Ports & bindings

Nothing listens on the Wi-Fi/LAN interface. Devices reach the demo only through the tunnel (decision I2).

| Port | Bound to | Service | Environment |
| ------ | ---------- | --------- | ------------- |
| 5432 | `127.0.0.1` | PostgreSQL (dev compose) | dev |
| 8080 | all interfaces (Go default) — keep Windows Firewall blocking inbound | Go API (`go run`) | dev |
| 80 | `127.0.0.1` | Nginx (dev compose, only if started) | dev |
| 3000 | Next.js default | POS frontend dev server (separate repo) | dev / demo |
| 8088 | `127.0.0.1` | Nginx (demo) | demo |
| 55432 | `127.0.0.1` | throwaway PostgreSQL | test |
| — | outbound 443/7844 only | `cloudflared` → Cloudflare edge | demo |

`./scripts/dev.ps1 doctor` lists what is listening and warns about firewall state.

## 3. Windows Firewall

The tunnel needs **no inbound rule**: `cloudflared` only makes outbound connections.

- Keep all three profiles (Domain, Private, **Public**) enabled. On 2026-10-31 the Public profile
  was found **disabled** while connected to a Public Wi-Fi; enable it in an elevated PowerShell:

  ```powershell
  Set-NetFirewallProfile -Profile Domain,Private,Public -Enabled True
  ```

- If Windows asks whether `go.exe`/`main.exe` may accept connections, choose **Cancel / Private only**.
  Nothing in this setup needs LAN access.
- Docker Desktop publishes ports through its own proxy; the `127.0.0.1:` prefix in the compose
  files is what keeps them off the network, not the firewall.

## 4. Request path (demo)

```text
Browser / tablet (HTTPS)
  -> Cloudflare edge (TLS, *.trycloudflare.com)
  -> cloudflared container (outbound tunnel, edge subnet 172.29.10.0/24)
  -> nginx (nginx.demo.conf)
       /api/v1/auth/staff/login  -> api:8080   auth_limit 10 r/min/IP, burst 5
       /api/v1/payments/...      -> api:8080   webhook_limit
       /api/*                    -> api:8080   api_limit 30 r/s/IP
       /ws                       -> api:8080   WebSocket upgrade, 1 h idle
       /health*                  -> api:8080
       everything else           -> host.docker.internal:3000 (Next.js; 503 page if not running)
  -> api (Go): TRUSTED_PROXIES = 172.29.10.0/24, so ClientIP() is the visitor IP
```

- **Proxy trust:** Nginx restores the visitor IP from `X-Forwarded-For` only when the peer is in the
  edge subnet (`set_real_ip_from`), and the API trusts the same subnet (`TRUSTED_PROXIES`). If you
  change `DEMO_EDGE_SUBNET`, also change `set_real_ip_from` in `deploy/nginx/nginx.demo.conf`.
- **One origin:** frontend and API share the tunnel hostname, so the `SameSite=Strict` refresh cookie
  works without a domain (decision I3) and no CORS is involved.
- **WebSocket:** `/ws` is proxied with upgrade headers and a 1-hour read timeout, but returns `503`
  in `staging` until the Phase 03 WebSocket ticket exists. Cloudflare Quick Tunnels support WebSockets.
- **Cafe NAT:** all tablets in one cafe share a public IP, so the strict limit applies only to
  `/login`; refresh, logout and `/me` use the normal API limit.

## 5. Daily workflow

```powershell
./scripts/dev.ps1 setup      # once: .env from template, DB, migrations, seed
./scripts/dev.ps1 run        # API on http://localhost:8080 (or F5 in VS Code)
./scripts/dev.ps1 login      # smoke test: staff login + /me with the dev PIN
./scripts/dev.ps1 test       # unit tests (-race)
./scripts/dev.ps1 test-int   # PostgreSQL 18 integration tests (same as CI)
./scripts/dev.ps1 check      # gofmt, vet, mod tidy -diff, govulncheck, tests
./scripts/dev.ps1 reset-db   # wipe the DEV volume and re-seed
./scripts/dev.ps1 demo-up    # demo stack + print the public URL
./scripts/dev.ps1 demo-url   # print the current URL (changes when the tunnel restarts)
./scripts/dev.ps1 demo-down
```

`dev.ps1 migrate|seed|run` always target the local Docker PostgreSQL: they build `DATABASE_URL` and
`DATABASE_MIGRATION_URL` from the `DB_*` values in `.env` with host `127.0.0.1` and ignore any
remote URL in `.env`. VS Code F5 (`.vscode/launch.json`) loads `.env` as-is, so keep
`DATABASE_URL` in `.env` pointing at `127.0.0.1` before debugging.

Dev login (seed only, never production): store `central-cafe` or `express-counter`, emails in
`db/seeds/development.sql`, PIN `482913`. Five wrong PINs lock an account for 15 minutes.

## 6. Creating a real store (operator only)

Production has no seed and no sign-up endpoint (decision I5). On the server, with the migrator DSN:

```powershell
go run ./cmd/migrate -action=bootstrap-store -store-slug kopi-senja -store-name "Kopi Senja" -owner-name "Nama Owner" -owner-email owner@kopisenja.id
```

The PIN is asked twice without echo. Break-glass PIN reset for an owner (decision I6) revokes all
of that owner's sessions and writes `STAFF_PIN_RESET`:

```powershell
go run ./cmd/migrate -action=reset-owner-pin -store-slug kopi-senja -owner-email owner@kopisenja.id
```

In containers, override the migrate service entrypoint:
`docker compose -f deploy/docker/docker-compose.prod.yml run --rm -it --entrypoint /app/mycaffe-migrate migrate -action=bootstrap-store ...`
