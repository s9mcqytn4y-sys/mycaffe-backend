# Backup & Restore Runbook (per client VPS)

Target: **RPO ≤ 1 hour, RTO ≤ 4 hours** (audit ticket P0-02). Status: scripts and CI round-trip *Implemented & Tested*; first real drill on a client VPS *Planned* (no VPS yet).

## 1. What runs

| Job | Schedule (UTC) | Script | Log |
|-----|----------------|--------|-----|
| Encrypted backup | every hour at :15 | `scripts/backup/backup_to_r2.sh` | `/var/log/mycaffe/backup.log` |
| Restore drill | 2nd of month 20:30 (03:30 WIB) | `scripts/backup/restore_drill.sh` | `/var/log/mycaffe/restore-drill.log` |

Both are installed from [`deploy/backup/mycaffe-backup.cron`](../../deploy/backup/mycaffe-backup.cron) and share one `flock`, so a drill never overlaps a backup.

**Backup**: `pg_dump -Fc` inside `mycaffe-postgres` as `mycaffe_backup` (read-only, `BYPASSRLS`, no password, local socket only) → GPG AES-256 → SHA-256 manifest → R2 `backups/hourly/`. The first backup of each UTC day and month is copied to `backups/daily/` and `backups/monthly/`.

**Drill**: restores the newest hourly backup into a throwaway `postgres:18-alpine` container, checks that `mycaffe_app` can read the tables (GRANTs restored), compares migrations and row counts with the live database, prints `RESULT PASS|FAIL ... duration_s=...`, and deletes the container. The live database is only read.

## 2. One-time setup on a new VPS

1. R2 bucket per client, e.g. `mycaffe-backups-<client>`, with an API token limited to that bucket (Object Read & Write).
2. R2 lifecycle rules on that bucket (Cloudflare dashboard → R2 → bucket → Settings → Object lifecycle rules):

   | Prefix | Delete after |
   |--------|--------------|
   | `backups/hourly/` | 2 days |
   | `backups/daily/` | 30 days |
   | `backups/monthly/` | 365 days |

3. Host packages: `docker`, `gpg`, `awscli` (v2), `openssl`, `util-linux` (`flock`).
4. OS user `mycaffe` in the `docker` group; `/opt/mycaffe` = files of the released tag (`deploy/`, `scripts/`); `mkdir -p /var/log/mycaffe && chown mycaffe /var/log/mycaffe`.
5. `/etc/mycaffe/backup.env` (owner `mycaffe`, mode `600`), values from the password manager, never committed:

   ```bash
   DB_CONTAINER=mycaffe-postgres
   DB_NAME=mycaffe_prod
   R2_ENDPOINT=https://<account_id>.r2.cloudflarestorage.com
   R2_BACKUP_BUCKET=mycaffe-backups-<client>
   R2_BACKUP_PREFIX=backups
   AWS_ACCESS_KEY_ID=<r2 token id>
   AWS_SECRET_ACCESS_KEY=<r2 token secret>
   AWS_DEFAULT_REGION=auto
   BACKUP_ENCRYPTION_PASSPHRASE=<high-entropy passphrase, also stored offline>
   ```

   Losing the passphrase makes every backup unreadable. Keep a copy outside the VPS.

6. `install -m 644 /opt/mycaffe/deploy/backup/mycaffe-backup.cron /etc/cron.d/mycaffe-backup`
7. As user `mycaffe`, run the two commands from the cron file once by hand and keep the drill's `RESULT PASS` line.

The `mycaffe_backup` role is created by `deploy/postgres/00-init-roles.sh` on the first start of an empty data volume. On a volume initialised before this change, run that script once as the superuser (it is idempotent).

## 3. Disaster recovery (VPS lost)

1. New VPS: §2 steps 3–5, then `docker compose -f deploy/docker/docker-compose.prod.yml up -d postgres` with the same `.env` (roles are created on the empty volume).
2. Restore the newest backup into it:

   ```bash
   set -a; . /etc/mycaffe/backup.env; set +a
   BACKUP_OBJECT=latest DB_MIGRATOR_USER=mycaffe_migrator APP_ENV=production CONFIRM_DESTRUCTIVE_RESTORE=true \
     /opt/mycaffe/scripts/backup/restore_from_r2.sh
   ```

   A specific backup: `BACKUP_OBJECT=daily/backup_mycaffe_prod_20261102_001500Z.dump.gpg`.
3. Start the API with the same image digest as before (`migrate -action=up` is a no-op if the backup is current), point the Cloudflare Tunnel (IN1) at the new VPS, run the smoke test.
4. Record the incident: time lost (RPO actually achieved), time to recover (RTO), backup object used.

## 4. Why the restore keeps privileges and skips extensions

- Privileges: restoring with `--no-privileges` drops the GRANTs to `mycaffe_app`; the API then fails with `permission denied for table orders` (reproduced 2026-11-02). The script restores them.
- Extensions: `uuid-ossp` belongs to the superuser and is created by `00-init-roles.sh`; `--clean` would try to drop it as `mycaffe_migrator` and abort the restore, so extension entries are filtered out of the restore list.

## 5. Not covered yet

- Alert when the newest backup is older than 2 hours or a drill fails: ticket P0-04 (Grafana Cloud, IN3).
- RPO below 1 hour (WAL archiving): ticket P2-08, only if a client contract requires it.
