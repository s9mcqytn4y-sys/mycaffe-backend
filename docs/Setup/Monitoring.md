# Monitoring and Alerting (P0-04, decision IN3)

Technical monitoring for one client VPS: Grafana Alloy on the VPS ships metrics to a Grafana Cloud (free tier) stack, alert rules run there, and alerts reach the operator's Telegram group and email. Store owners do not get these alerts; their business notifications come from MyCaffe itself.

```
api :9091/metrics ──(mycaffe-metrics, internal)──┐
host /proc /sys / (read-only) ───────────────────┼─ alloy ──HTTPS remote write──► Grafana Cloud
/var/lib/mycaffe/metrics/*.prom (backup, drill) ─┘                                 rules ─► Telegram, email
```

| File | Purpose |
|------|---------|
| `deploy/monitoring/config.alloy` | Scrape targets and remote write; mounted read-only into the `alloy` service |
| `deploy/monitoring/alert-rules.yml` | Alert rules (Prometheus format), loaded into Grafana Cloud |
| `deploy/monitoring/alert-rules.test.yml` | `promtool test rules` cases, run in CI (`docker-build` job) |
| `deploy/docker/docker-compose.prod.yml` | `alloy` service, `mycaffe-metrics` and `mycaffe-monitor-egress` networks |

Container logs are not shipped (that needs `docker.sock`, which is root on the host). Read them on the VPS with `docker compose -f deploy/docker/docker-compose.prod.yml logs api`; they are JSON lines (P0-03).

## 1. Alerts

| Alert | Fires when | Severity |
|-------|-----------|----------|
| `MyCaffeMetricsMissing` | no API metrics for 10 min (VPS, Alloy or Grafana credentials down) | critical |
| `MyCaffeAPIDown` | API scrape fails for 2 min | critical |
| `MyCaffeHigh5xxRate` | >5% of requests are 5xx for 5 min, with at least 3 req/min | critical |
| `MyCaffeSlowRequests` | p95 latency >1 s for 10 min (`/ws` excluded) | warning |
| `MyCaffeWorkerStalled` | reconciliation, email or outbox relay has not finished a cycle for >5 min | critical |
| `MyCaffeEmailPermanentFailure` | an email was marked failed in the last 30 min | warning |
| `MyCaffeDBPoolSaturated` | >90% of the 25 DB connections in use for 5 min | warning |
| `MyCaffeBackupStale` | newest successful backup is >2 h old | critical |
| `MyCaffeBackupMetricMissing` | no backup recorded for 2 h while host metrics arrive | critical |
| `MyCaffeRestoreDrillStale` | no passing restore drill in 35 days | warning |
| `MyCaffeDiskLow` / `MyCaffeDiskCritical` | free disk <15% for 15 min / <5% for 5 min | warning / critical |
| `MyCaffeMemoryLow` | available RAM <10% for 15 min | warning |

Every series carries `client=<MYCAFFE_CLIENT>`, so one Grafana stack can watch several client VPSs.

## 2. Grafana Cloud setup (once per operator stack)

Steps in the Grafana Cloud UI change over time; follow the current UI if a menu name differs.

1. Create the free stack. Under the stack's Prometheus details, note the remote write URL and the instance ID (username).
2. Create two access policy tokens: one with `metrics:write` only (for Alloy on each VPS), one with `rules:write` (for loading rules from the operator's laptop; never put it on a VPS).
3. Load the rules, from a checkout of the released tag:

   ```bash
   mimirtool rules load --address=<prometheus URL without /api/prom/push> --id=<instance id> --key=<rules:write token> deploy/monitoring/alert-rules.yml
   ```

4. Contact points (Alerting → Contact points, on the stack's Mimir/Prometheus Alertmanager, since these are data-source-managed rules): a Telegram contact point (bot token from @BotFather, chat id of the operator group) and an email contact point.
5. Notification policy: `severity=critical` → Telegram and email; `severity=warning` → Telegram. Group by `client, alertname`.
6. Uptime check (needs the production domain): Testing & synthetics → Synthetic Monitoring → HTTP check on `https://<domain>/health/ready` from at least two probe locations, with alerting enabled. This catches an outage even when the whole VPS, and therefore Alloy, is gone.
7. Check the free-tier active series quota in the stack's usage page after the first day of traffic.

## 3. VPS setup (per client)

1. `mkdir -p /var/lib/mycaffe/metrics && chown mycaffe /var/lib/mycaffe/metrics` before the first `docker compose up` (otherwise Docker creates it owned by root and the backup cron cannot write its metric, which `MyCaffeBackupMetricMissing` will report).
2. Add to the production `.env` (values from the password manager):

   ```bash
   MYCAFFE_CLIENT=<client slug>
   GRAFANA_CLOUD_PROM_URL=<remote write URL>
   GRAFANA_CLOUD_PROM_USER=<instance id>
   GRAFANA_CLOUD_API_KEY=<metrics:write token>
   ```

3. `docker compose -f deploy/docker/docker-compose.prod.yml up -d alloy`
4. Verify in Grafana Explore: `up{client="<client slug>"}` shows `job="mycaffe-api"` and `job="integrations/unix"` at 1, and `mycaffe_backup_last_success_timestamp_seconds{client="<client slug>"}` appears after the next hourly backup.
5. Test the route once: temporarily stop the API (`docker compose stop api` during a maintenance window) and confirm `MyCaffeAPIDown` reaches Telegram within ~3 minutes, then start it again.

## 4. Notes

- The API listens for metrics on `:9091` inside its container. No host port is published and Nginx does not route it; containers on `mycaffe-edge` (nginx, web) could reach it, and the metrics carry no ids, tokens or customer data (P0-03).
- The root filesystem is mounted with `rslave` so new host mounts show up; this needs a shared `/` mount, which is the Ubuntu/systemd default (Docker Desktop does not support it).
- A worker's last-success gauge appears after its first completed cycle, so a worker that fails from its very first cycle after a restart is not alerted by `MyCaffeWorkerStalled`; it only shows in the API logs (`[EMAIL WORKER]`, `[RECONCILIATION WORKER]` lines).
