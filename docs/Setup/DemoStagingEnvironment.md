# Demo / Staging Environment (Cloudflare Quick Tunnel)

A self-healing demo stack that runs on a local workstation and is reachable from the internet
through a Cloudflare Quick Tunnel. Defined in `deploy/docker/docker-compose.demo.yml`.

| Service | Role | Restart |
| --------- | ------ | --------- |
| `postgres` | PostgreSQL 18, triple-role init (`00-init-roles.sh`) | `unless-stopped` + healthcheck |
| `migrate` | Builds `mycaffe-api:demo`, runs `migrate up` then the idempotent development seed | runs once per `up` |
| `api` | MyCaffe API, `APP_ENV=staging`, Midtrans sandbox | `unless-stopped` + healthcheck |
| `nginx` | Reverse proxy (`deploy/nginx/nginx.demo.conf`): `/api`, `/ws`, `/health` → API, everything else → Next.js on the host (`host.docker.internal:3000`); bound to `127.0.0.1:8088` only | `unless-stopped` + healthcheck |
| `tunnel` | `cloudflared` Quick Tunnel → `http://nginx:80` | `unless-stopped` + healthcheck |

## Start

```bash
docker compose -f deploy/docker/docker-compose.demo.yml up -d --build
```

## Get the public URL

```bash
docker compose -f deploy/docker/docker-compose.demo.yml logs tunnel | grep trycloudflare.com
```

The Quick Tunnel URL (`https://<random>.trycloudflare.com`) **changes whenever the `tunnel`
container restarts**. For a stable hostname, create a Named Tunnel in the Cloudflare dashboard
(needs the domain from decision O2), put its token in your local `.env` as
`CLOUDFLARE_TUNNEL_TOKEN`, and change the `tunnel` command to
`tunnel --no-autoupdate run --token ${CLOUDFLARE_TUNNEL_TOKEN}`. Never commit the token.

## Auto-recovery

- A crashed or killed `api`, `nginx`, `postgres`, or `tunnel` container is restarted by Docker
  (verified 2026-10-31: `api` and `nginx` killed with SIGTERM came back healthy, `RestartCount=1`).
- To survive a Windows reboot, enable **Docker Desktop → Settings → General → Start Docker Desktop
  when you sign in**. Containers with `unless-stopped` start again with Docker.
- `docker compose stop`/`down` is a manual stop: those containers stay stopped until the next `up`.

## What the demo exposes

The demo runs the same image as production with `APP_ENV=staging`. Since iteration 5 every route is
live, including guest QR ordering (`/api/v1/public/*`) and `GET /ws` (single-use tickets). Full list:
[FrontendIntegrationContract.md](../Architecture/FrontendIntegrationContract.md).

**Before sharing the URL**, close the leaks in
[DemoReadinessChecklist.md](DemoReadinessChecklist.md) §1:

- the development seed uses PIN `482913` for every account, owner included;
- `JWT_SECRET_KEY` has a public default in the compose file;
- the seed contains a personal e-mail address.

Media uploads answer `503 MEDIA_UNAVAILABLE` until `R2_*` is set.

## Client IP & proxies

The edge network has a fixed subnet (`DEMO_EDGE_SUBNET`, default `172.29.10.0/24`) and the API
trusts only that subnet (`TRUSTED_PROXIES`), so logs show the real visitor IP forwarded by
`cloudflared` instead of a container address. Nginx restores the same visitor IP with
`set_real_ip_from 172.29.10.0/24` (`nginx.demo.conf`), so `limit_req` is per visitor, not one
shared bucket for all tunnel traffic. Keep both in sync if you change `DEMO_EDGE_SUBNET`.
Full networking notes: [WindowsDevEnvironment.md](WindowsDevEnvironment.md). Email in the demo: [Email.md](Email.md).

## Midtrans sandbox webhook

Set the sandbox Payment Notification URL to `<tunnel-url>/api/v1/payments/midtrans/webhook` and put
your sandbox keys in the local `.env` (`MIDTRANS_SERVER_KEY`, `MIDTRANS_CLIENT_KEY`). Update the URL
after each tunnel restart.

## Reset

```bash
docker compose -f deploy/docker/docker-compose.demo.yml down -v
```

`-v` deletes the demo database volume (`demo-pgdata`). Only demo seed data lives there.
