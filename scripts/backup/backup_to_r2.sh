#!/usr/bin/env bash
# ==============================================================================
# MyCaffe POS — Automated Offsite Database Backup to Cloudflare R2
# Authenticated encryption (GPG AES256 + SHA512 MDC) + SHA-256 checksum manifest
# ==============================================================================
set -euo pipefail

# Required environment variables:
# DB_CONTAINER (default: mycaffe-postgres)
# DB_USER (default: mycaffe_backup — read-only BYPASSRLS role from 00-init-roles.sh, local socket only)
# DB_NAME (default: mycaffe_prod)
# R2_BACKUP_BUCKET (default: mycaffe-backups)
# R2_ENDPOINT (e.g. https://<account_id>.r2.cloudflarestorage.com)
# AWS_ACCESS_KEY_ID (R2 backup access token ID)
# AWS_SECRET_ACCESS_KEY (R2 backup access token secret)
# BACKUP_ENCRYPTION_PASSPHRASE (High-entropy passphrase for GPG symmetric AES256)

DB_CONTAINER="${DB_CONTAINER:-mycaffe-postgres}"
DB_USER="${DB_USER:-mycaffe_backup}"
DB_NAME="${DB_NAME:-mycaffe_prod}"
R2_BACKUP_BUCKET="${R2_BACKUP_BUCKET:-${R2_BUCKET:-mycaffe-backups}}"
R2_BACKUP_PREFIX="${R2_BACKUP_PREFIX:-backups}"
TIMESTAMP="$(date -u +%Y%m%d_%H%M%SZ)"
BACKUP_DIR="/tmp/mycaffe-backups"
BACKUP_FILE="${BACKUP_DIR}/backup_${DB_NAME}_${TIMESTAMP}.dump.gpg"
CHECKSUM_FILE="${BACKUP_FILE}.sha256"

if [ -z "${R2_BACKUP_BUCKET:-}" ] || [ -z "${R2_ENDPOINT:-}" ]; then
    echo "[BACKUP ERROR] R2_BACKUP_BUCKET and R2_ENDPOINT must be set." >&2
    exit 1
fi

if [ -z "${BACKUP_ENCRYPTION_PASSPHRASE:-}" ]; then
    echo "[BACKUP ERROR] BACKUP_ENCRYPTION_PASSPHRASE must be set for authenticated encrypted backups." >&2
    exit 1
fi

mkdir -p "${BACKUP_DIR}"
chmod 700 "${BACKUP_DIR}"

cleanup() {
    rm -f "${BACKUP_FILE}" "${CHECKSUM_FILE}"
}
trap cleanup EXIT

echo "[BACKUP] Starting PostgreSQL custom-format dump from container ${DB_CONTAINER} at ${TIMESTAMP}..."
docker exec "${DB_CONTAINER}" pg_dump -U "${DB_USER}" -d "${DB_NAME}" -F c \
    | gpg --batch --yes --symmetric \
          --cipher-algo AES256 \
          --digest-algo SHA512 \
          --s2k-mode 3 \
          --s2k-count 65011712 \
          --passphrase-fd 3 \
          --output "${BACKUP_FILE}" 3<<< "${BACKUP_ENCRYPTION_PASSPHRASE}"

FILESIZE=$(stat -c%s "${BACKUP_FILE}" 2>/dev/null || stat -f%z "${BACKUP_FILE}")
if [ "${FILESIZE}" -le 64 ]; then
    echo "[BACKUP ERROR] Encrypted backup file is unexpectedly small (${FILESIZE} bytes)." >&2
    exit 1
fi

(
    cd "${BACKUP_DIR}"
    sha256sum "$(basename "${BACKUP_FILE}")" > "$(basename "${CHECKSUM_FILE}")"
)
echo "[BACKUP] Authenticated GPG encryption completed. File size: ${FILESIZE} bytes."

# Retention tiers: every run lands in hourly/; the first successful run of a UTC day / month is also
# copied to daily/ and monthly/. R2 lifecycle rules expire each prefix (docs/Setup/BackupAndRestore.md).
BASE="s3://${R2_BACKUP_BUCKET}/${R2_BACKUP_PREFIX}"
NAME="$(basename "${BACKUP_FILE}")"
s3() { aws --endpoint-url "${R2_ENDPOINT}" s3 "$@"; }

echo "[BACKUP] Uploading encrypted archive and SHA-256 manifest to ${BASE}/hourly/..."
s3 cp --only-show-errors "${BACKUP_FILE}" "${BASE}/hourly/${NAME}"
s3 cp --only-show-errors "${CHECKSUM_FILE}" "${BASE}/hourly/${NAME}.sha256"

promote() { # tier, name prefix that identifies the period
    if ! s3 ls "${BASE}/$1/$2" >/dev/null 2>&1; then
        s3 cp --only-show-errors "${BASE}/hourly/${NAME}" "${BASE}/$1/${NAME}"
        s3 cp --only-show-errors "${BASE}/hourly/${NAME}.sha256" "${BASE}/$1/${NAME}.sha256"
        echo "[BACKUP] Promoted to $1/."
    fi
}
promote daily "backup_${DB_NAME}_${TIMESTAMP:0:8}"
promote monthly "backup_${DB_NAME}_${TIMESTAMP:0:6}"

echo "[BACKUP] Backup and integrity manifest uploaded successfully at $(date -u +%Y%m%d_%H%M%SZ)."

# Freshness for the MyCaffeBackupStale alert (read by Alloy's textfile collector). Written via
# rename so a scrape never sees a half-written file; a missing directory only skips the metric.
METRICS_DIR="${METRICS_TEXTFILE_DIR:-/var/lib/mycaffe/metrics}"
if [[ -d "${METRICS_DIR}" ]]; then
    printf '# TYPE mycaffe_backup_last_success_timestamp_seconds gauge\nmycaffe_backup_last_success_timestamp_seconds %s\n# TYPE mycaffe_backup_last_size_bytes gauge\nmycaffe_backup_last_size_bytes %s\n' \
        "$(date -u +%s)" "${FILESIZE}" > "${METRICS_DIR}/backup.prom.$$" && mv "${METRICS_DIR}/backup.prom.$$" "${METRICS_DIR}/backup.prom"
fi
