#!/usr/bin/env bash
# ==============================================================================
# MyCaffe POS — Automated Offsite Database Backup to Cloudflare R2
# Authenticated encryption (GPG AES256 + SHA512 MDC) + SHA-256 checksum manifest
# ==============================================================================
set -euo pipefail

# Required environment variables:
# DB_CONTAINER (default: mycaffe-postgres)
# DB_USER (default: mycaffe_migrator)
# DB_NAME (default: mycaffe_prod)
# R2_BACKUP_BUCKET (default: mycaffe-backups)
# R2_ENDPOINT (e.g. https://<account_id>.r2.cloudflarestorage.com)
# AWS_ACCESS_KEY_ID (R2 backup access token ID)
# AWS_SECRET_ACCESS_KEY (R2 backup access token secret)
# BACKUP_ENCRYPTION_PASSPHRASE (High-entropy passphrase for GPG symmetric AES256)

DB_CONTAINER="${DB_CONTAINER:-mycaffe-postgres}"
DB_USER="${DB_USER:-mycaffe_migrator}"
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

echo "[BACKUP] Uploading encrypted archive and SHA-256 manifest to Cloudflare R2 bucket: ${R2_BACKUP_BUCKET}/${R2_BACKUP_PREFIX}..."
aws --endpoint-url "${R2_ENDPOINT}" s3 cp "${BACKUP_FILE}" "s3://${R2_BACKUP_BUCKET}/${R2_BACKUP_PREFIX}/$(basename "${BACKUP_FILE}")"
aws --endpoint-url "${R2_ENDPOINT}" s3 cp "${CHECKSUM_FILE}" "s3://${R2_BACKUP_BUCKET}/${R2_BACKUP_PREFIX}/$(basename "${CHECKSUM_FILE}")"

echo "[BACKUP] Backup and integrity manifest uploaded successfully at $(date -u +%Y%m%d_%H%M%SZ)."
