# Production Deploy and Rollback (P0-05)

Production runs images pinned by digest (`ghcr.io/...@sha256:...`), never by a mutable tag. `scripts/deploy/deploy.sh` is the only way new images reach a VPS.

## 1. Get the digest

1. A merge to `main` (or a `v*.*.*` tag) runs **Release - Publish Immutable Container Image to GHCR**.
2. The run summary shows `Digest: sha256:...` and the exact `deploy.sh` command. Copy the digest from there, not from a tag.
3. For a `v*` tag the GitHub Release lists the same digest, with the SBOM and SLSA provenance attached.
4. The web image digest comes from the frontend repository's release run the same way.

## 2. Deploy

On the VPS, as user `mycaffe`, from `/opt/mycaffe` (the released tag's `deploy/` and `scripts/`, see [backup-and-restore.md](backup-and-restore.md) §2):

```bash
scripts/deploy/deploy.sh sha256:<api digest>                      # web keeps its running digest
scripts/deploy/deploy.sh sha256:<api digest> sha256:<web digest>  # first deploy, or a new web image
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

## 5. If the rollback also fails (exit code 2)

1. `cat /var/lib/mycaffe/deploy/history.log` and `scripts/deploy/compose.sh logs --tail 300 migrate api`.
2. A migration that broke the old image is a migration bug: restore the pre-deploy backup ([backup-and-restore.md](backup-and-restore.md) §3), then deploy the last good digest.
3. Never edit files or rebuild images on the VPS (ADR-002 §4).

## 6. Limits

- Only `api` health decides success. A broken `web` image is not rolled back automatically (it has no healthcheck yet); fix forward or run `deploy.sh rollback`.
- `postgres`, `nginx`, `certbot` and `alloy` images are not managed by `deploy.sh`.
- Tested in CI with a local registry (`scripts/deploy/test_deploy_rollback.sh`). The first run on a real VPS still needs a manual check.
