# Production Deploy and Rollback (P0-05)

Production runs images pinned by digest (`ghcr.io/...@sha256:...`), never by a mutable tag. `scripts/deploy/deploy.sh` is the only way new images reach a VPS.

## 1. Get the digest

1. A merge to `main` (or a `v*.*.*` tag) runs **Release - Publish Immutable Container Image to GHCR**.
2. The run summary shows `Digest: sha256:...` and the exact `deploy.sh` command. Copy the digest from there, not from a tag.
3. For a `v*` tag the GitHub Release lists the same digest, with the SBOM and SLSA provenance attached.
4. The web image digest comes from the frontend repository's release run the same way.

## 1a. Ingress (IN1, P0-07)

Production publishes **no ports**. A Named Cloudflare Tunnel (`tunnel` service, token in
`CLOUDFLARE_TUNNEL_TOKEN`) carries traffic to Nginx on the internal edge network. TLS ends at
Cloudflare, so there is no Certbot and no certificate volume. The firewall and provisioning steps are in
[OperationsRunbook.md](OperationsRunbook.md).

## 2. Deploy

On the VPS, as user `mycaffe`, from `/opt/mycaffe` (the released tag's `deploy/` and `scripts/`, see [BackupAndRestore.md](BackupAndRestore.md) §2):

```bash
scripts/deploy/deploy.sh sha256:<api digest>                      # web keeps its running digest; API-only while none was given
scripts/deploy/deploy.sh sha256:<api digest> sha256:<web digest>  # adds or updates the optional web frontend (profile `web`, W3)
```

What it does, in order:

1. Refuses anything that is not `sha256:<64 hex>`, and a second deploy running at the same time.
2. Encrypted backup to R2 (`/etc/mycaffe/backup.env`, same lock as the hourly cron). A failed backup stops the deploy before anything changes.
3. `docker compose pull`, then `up -d`. The `migrate` service applies additive migrations as `mycaffe_migrator` before `api` starts.
4. Waits up to 180 s (`DEPLOY_HEALTH_TIMEOUT_S`) for the `api` container on the new digest to be `healthy` (`/health/ready`).
5. Success: writes `/var/lib/mycaffe/deploy/current.env`, moves the old one to `previous.env`, appends to `history.log`.
6. Failure in pull, migrate, start or health: redeploys the previous digests and exits 1. The deploy is recorded as `FAILED ... rolled back` in `history.log`.

Migrations stay additive (AGENTS.md), so the previous image keeps working on a schema that a failed deploy already migrated.

## 3. Rollback by hand

```bash
scripts/deploy/deploy.sh rollback
```

Redeploys `previous.env` (with a backup first). Running it twice returns to where you started.

## 4. Other compose commands

The compose file requires the digests, so use the wrapper that reads `current.env`:

```bash
scripts/deploy/compose.sh ps
scripts/deploy/compose.sh logs --tail 200 api
scripts/deploy/compose.sh up -d alloy
```

## 5. Daily purge of technical rows (P0-08)

`migrate -action=purge` runs as `mycaffe_migrator` and deletes only technical rows past retention.
It logs the deleted count per table.

| Table | Deleted when |
|-------|--------------|
| `outbox_events` | Processed (or dead-lettered) more than 7 days ago |
| `ws_tickets` | Expired more than 1 day ago |
| `guest_sessions` | Expired more than 30 days ago and referenced by no order or ticket |
| `receipt_tokens`, `account_tokens`, `auth_sessions` | Expired more than 30 days ago |
| `email_outbox` | Sent or failed, created more than 90 days ago |
| `media_uploads` | Still pending 1 day after expiry |
| `otp_challenges` | Expired more than 1 day ago |
| `message_outbox` | Sent or failed, created more than 90 days ago |

The job never purges money, audit or webhook evidence: payments, ledgers, `payment_notification_logs`,
`audit_logs` and `approval_requests` are kept.

The cron line is in `deploy/backup/mycaffe-backup.cron`, daily at 20:00 UTC (03:00 WIB). It reads the
deployed digest from `/var/lib/mycaffe/deploy/current.env`.

## 6. If the rollback also fails (exit code 2)

1. `cat /var/lib/mycaffe/deploy/history.log` and `scripts/deploy/compose.sh logs --tail 300 migrate api`.
2. A migration that broke the old image is a migration bug: restore the pre-deploy backup ([BackupAndRestore.md](BackupAndRestore.md) §3), then deploy the last good digest.
3. Never edit files or rebuild images on the VPS (ADR-002 §4).

## 7. Limits

- Only `api` health decides success. A broken `web` image is not rolled back automatically (it has no healthcheck yet); fix forward or run `deploy.sh rollback`.
- `postgres`, `nginx`, `certbot` and `alloy` images are not managed by `deploy.sh`.
- Tested in CI with a local registry (`scripts/deploy/test_deploy_rollback.sh`). The first run on a real VPS still needs a manual check.
