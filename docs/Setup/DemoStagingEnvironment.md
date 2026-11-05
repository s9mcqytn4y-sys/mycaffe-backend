# Demo / Staging Environment (`https://demo.abbdullaziz.online`)

A self-healing demo stack that runs on a local workstation (Windows 11 + Docker Desktop) and is reachable from the internet through a persistent **Cloudflare Named Tunnel** (`mycaffe-demo` -> `https://demo.abbdullaziz.online`). Defined in [`deploy/docker/docker-compose.demo.yml`](file:///c:/Projects/myCaffe-backend/deploy/docker/docker-compose.demo.yml).

| Service | Role | Restart |
| --------- | ------ | --------- |
| `postgres` | PostgreSQL 18, triple-role init (`00-init-roles.sh`) | `unless-stopped` + healthcheck |
| `migrate` | Builds `mycaffe-api:demo`, runs `migrate up` (29 migrations), idempotent seed, and `demo-pins` rotation | runs once per `up` |
| `api` | MyCaffe API, `APP_ENV=staging`, Midtrans Sandbox Core API, Gmail SMTP, SMS-to-Email Bridge, Sentry, R2 Media (`https://cdn.abbdullaziz.online`) | `unless-stopped` + healthcheck |
| `nginx` | Reverse proxy ([`deploy/nginx/nginx.demo.conf`](file:///c:/Projects/myCaffe-backend/deploy/nginx/nginx.demo.conf)): `/api`, `/ws`, `/health` → API, everything else → Next.js on the host (`host.docker.internal:3000`); bound to `127.0.0.1:8088` only | `unless-stopped` + healthcheck |
| `tunnel` | `cloudflared` Named Tunnel (`mycaffe-demo`, ID `85b97ba7-79d7-48d7-b045-b62645435708`) → `http://nginx:80` | `unless-stopped` + healthcheck |

## Start

1. Copy [`.env.demo.example`](file:///c:/Projects/myCaffe-backend/.env.demo.example) to `.env.demo` (git-ignored) and set `JWT_SECRET_KEY` (`openssl rand -hex 32`), `DEMO_OWNER_PIN`, `DEMO_STAFF_PIN`, Midtrans Sandbox keys, Gmail SMTP credentials, R2 keys, Sentry DSN, and `CLOUDFLARE_TUNNEL_TOKEN`.
2. Start the stack:

```powershell
docker compose --env-file .env.demo -f deploy/docker/docker-compose.demo.yml up -d --build
```

## Public URL & Cloudflare Named Tunnel (`abbdullaziz.online`)

- **Named Tunnel (`mycaffe-demo`)**: Configured with `CLOUDFLARE_TUNNEL_TOKEN` in `.env.demo`, routing `https://demo.abbdullaziz.online` directly to `http://nginx:80`. Unlike a Quick Tunnel, the hostname `https://demo.abbdullaziz.online` is permanent and never changes across restarts.
- **R2 Public Media CDN**: Connected to custom domain `https://cdn.abbdullaziz.online` (`mycaffe-media` bucket) with strict CORS for `https://abbdullaziz.online` and `https://demo.abbdullaziz.online`.
- **Fallback Quick Tunnel**: If `CLOUDFLARE_TUNNEL_TOKEN` is omitted and `CLOUDFLARE_TUNNEL_COMMAND=--url http://nginx:80` is set, the `tunnel` container falls back to an ephemeral `https://*.trycloudflare.com` URL.

## Auto-recovery

- A crashed or killed `api`, `nginx`, `postgres`, or `tunnel` container is restarted automatically by Docker (`restart: unless-stopped`).
- To survive a Windows reboot, enable **Docker Desktop → Settings → General → Start Docker Desktop when you sign in**.

## What the Demo Exposes

The demo runs the production container image with `APP_ENV=staging`:
- All POS, KDS, Shift, Open Tab/Split Bill, Approval, Receipt, Report, Guest QR Ordering (`/api/v1/public/*`), and WebSocket (`GET /ws` with single-use tickets) routes are live.
- Leaks L-1..L-3 from [DemoReadinessChecklist.md](DemoReadinessChecklist.md) §1 are closed: the public seed PIN is replaced at every start (`DEMO_OWNER_PIN` / `DEMO_STAFF_PIN`), `JWT_SECRET_KEY` has no default, and staff emails are `@mycaffe.local` (except the owner account which uses the owner's Gmail per decision V6).
- **Email & Free SMS OTP**: `EMAIL_DRIVER=smtp` and `SMS_DRIVER=email` deliver both PIN reset emails and 6-digit SMS OTPs (`POST /api/v1/auth/staff/pin/forgot-sms`, `POST /api/v1/pos/customers/verify`) directly to the owner's Gmail inbox in real time.

## Midtrans Sandbox Webhook

Set the Midtrans Sandbox Payment Notification URL in the Midtrans Dashboard to:
```text
https://demo.abbdullaziz.online/api/v1/payments/midtrans/webhook
```

## Reset

```powershell
docker compose --env-file .env.demo -f deploy/docker/docker-compose.demo.yml down -v
```
`-v` deletes the demo database volume (`demo-pgdata`). Only demo seed data lives there.
