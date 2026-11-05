# Operations Runbook: Provisioning and Incidents (P0-09)

For the operator who sets up and runs one client's VPS. Deploy and rollback are in
[Deploy.md](Deploy.md). Backups are in [BackupAndRestore.md](BackupAndRestore.md). Alerts are in
[Monitoring.md](Monitoring.md).

## 1. Provision a new client VPS

Run once per client. Each step is checked before the next one.

| # | Step | Check |
|---|------|-------|
| 1 | Hostinger KVM 2, Ubuntu 24.04. Create user `mycaffe` (sudo, docker group); SSH key login only; `PasswordAuthentication no`, `PermitRootLogin no` | `ssh mycaffe@<vps>` works and a password login is refused |
| 2 | Firewall (§2) | `sudo ufw status verbose` shows deny incoming, with only 22 allowed from admin IPs |
| 3 | Install Docker Engine + Compose plugin; enable unattended security upgrades | `docker compose version` |
| 4 | Cloudflare: zone for the client domain → Zero Trust → Tunnels → create `mycaffe-<client>`; public hostnames `api.<domain>` and `pos.<domain>` → `http://nginx:80` | The tunnel shows *Healthy* after step 7 |
| 5 | Check out the released tag to `/opt/mycaffe` (`deploy/` and `scripts/` only, never built here). Create `/etc/mycaffe/prod.env` (mode 600) from `.env.example`: database passwords, `JWT_SECRET_KEY` (`openssl rand -hex 32`), Midtrans production keys, SMTP, `CLOUDFLARE_TUNNEL_TOKEN`, `ALLOWED_ORIGINS=https://pos.<domain>` | `docker compose --env-file /etc/mycaffe/prod.env -f deploy/docker/docker-compose.prod.yml config` passes |
| 6 | Backups: `/etc/mycaffe/backup.env`, then install `deploy/backup/mycaffe-backup.cron` to `/etc/cron.d/` | The first hourly backup appears in R2 |
| 7 | First deploy: `scripts/deploy/deploy.sh <api-digest> <web-digest>` | `https://api.<domain>/health/ready` returns 200 |
| 8 | Bootstrap the store: `docker compose ... run --rm --entrypoint /app/mycaffe-migrate migrate -action=bootstrap-store -store-slug=<slug> -store-name=... -owner-name=... -owner-email=...` (the PIN is read from the terminal, never from a flag) | The owner logs in |
| 9 | Midtrans dashboard: Payment Notification URL `https://api.<domain>/api/v1/payments/midtrans/webhook` | A sandbox or test transaction reaches `paid` |
| 10 | Restore drill once by hand: `scripts/backup/restore_drill.sh` | The drill reports success; record the date in the client sheet |
| 11 | Grafana Cloud: uptime check on `/health/ready`, alert rules imported ([Monitoring.md](Monitoring.md)) | A test alert reaches the operator Telegram |

## 2. Firewall (P0-07)

Since IN1 the VPS has **no inbound web ports**. Cloudflare reaches Nginx through the outbound tunnel.

```bash
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow from <admin-ip>/32 to any port 22 proto tcp
sudo ufw enable
```

Docker publishes no ports in `docker-compose.prod.yml`, so Docker's iptables rules cannot bypass ufw.
To check from outside: `nmap -Pn <vps-ip>` should show only port 22, filtered for everyone except
admin IPs.

The visitor IP that reaches the API is the Cloudflare `CF-Connecting-IP`, trusted only from the edge
subnet (`set_real_ip_from` in `nginx.prod.conf`, plus `TRUSTED_PROXIES`). If `EDGE_SUBNET` changes,
change both.

## 3. Incident playbook

Severity:

- **S1:** taking money is impossible, or data is wrong. Start within 15 minutes, any hour.
- **S2:** one feature is down. Start the same business day.
- **S3:** cosmetic.

| Symptom | First check | Action |
|---------|-------------|--------|
| Site unreachable | Cloudflare tunnel status; `docker compose ps` | `docker compose restart tunnel`; if the token was rotated, update `CLOUDFLARE_TUNNEL_TOKEN` and `up -d tunnel` |
| `/health/ready` 503 | `docker compose logs api --tail 200`; Postgres health | Restart `api`. If Postgres is down, check the disk (`df -h`); never delete volumes |
| Bad deploy | Error rate or alert right after a deploy | `scripts/deploy/deploy.sh rollback` ([Deploy.md](Deploy.md) §3) |
| QRIS orders stay `pending` | Midtrans dashboard notification log; `payment_notification_logs` | The reconciliation worker runs every 30 s; check that the webhook URL is correct. Never mark payments paid by hand |
| Refund stuck `requested` | `gateway_refunds` row; Midtrans transaction status | The worker checks after 2 minutes. If the bank denied it, refund manually (cash, outlet fund or transfer) |
| Emails or SMS not arriving | `email_outbox` / `message_outbox` `status = 'failed'`, `last_error` | Fix the SMTP or SMS credentials; failed rows are not retried, so the staff member requests a new link or code |
| Realtime screens frozen | `outbox_events` with `dead_at` set | Find the cause in `last_error`; screens recover by reconnecting (replay plus `resync_required`) |
| Data loss or corruption | Stop writes: `docker compose stop api` | Restore from R2 ([BackupAndRestore.md](BackupAndRestore.md)) to a new database, verify, then switch over |
| Suspected breach | Unexpected `audit_logs` entries, unknown sessions | Rotate `JWT_SECRET_KEY` (signs everyone out), revoke staff sessions, rotate DB, Midtrans and SMTP secrets, review `audit_logs` |

After every S1 or S2: write a short note (time line, cause, fix, follow-up ticket) in the client sheet
within 2 business days.
