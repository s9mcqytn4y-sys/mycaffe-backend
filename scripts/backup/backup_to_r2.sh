#!/usr/bin/env bash
# ==============================================================================
# MyCaffe POS — Automated Offsite Database Backup to Cloudflare R2
# Zero-egress encrypted disaster recovery backup
# ==============================================================================
set -euo pipefail

# Required environment variables:
# DB_CONTAINER (default: mycaffe-postgres)
# DB_USER (default: mycaffe_user)
# DB_NAME (default: mycaffe_prod)
# R2_BUCKET (e.g. mycaffe-backups)
# R2_ENDPOINT (e.g. https://<account_id>.r2.cloudflarestorage.com)
# AWS_ACCESS_KEY_ID (R2 access token ID)
# AWS_SECRET_ACCESS_KEY (R2 access token secret)
# BACKUP_ENCRYPTION_PASSPHRASE (Passphrase used for AES-256-CBC encryption)

DB_CONTAINER="${DB_CONTAINER:-mycaffe-postgres}"
DB_USER="${DB_USER:-mycaffe_user}"
DB_NAME="${DB_NAME:-mycaffe_prod}"
TIMESTAMP="$(date -u +%Y%m%d_%H%M%SZ)"
BACKUP_DIR="/tmp/mycaffe-backups"
BACKUP_FILE="${BACKUP_DIR}/backup_${DB_NAME}_${TIMESTAMP}.dump.enc"

if [ -z "${R2_BUCKET:-}" ] || [ -z "${R2_ENDPOINT:-}" ]; then
    echo "[BACKUP ERROR] R2_BUCKET and R2_ENDPOINT must be set." >&2
    exit 1
fi

if [ -z "${BACKUP_ENCRYPTION_PASSPHRASE:-}" ]; then
    echo "[BACKUP ERROR] BACKUP_ENCRYPTION_PASSPHRASE must be set for encrypted backups." >&2
    exit 1
fi

mkdir -p "${BACKUP_DIR}"

echo "[BACKUP] Starting PostgreSQL dump from container ${DB_CONTAINER} at ${TIMESTAMP}..."
docker exec "${DB_CONTAINER}" pg_dump -U "${DB_USER}" -d "${DB_NAME}" -F c \
    | openssl enc -aes-256-cbc -salt -pbkdf2 -pass env:BACKUP_ENCRYPTION_PASSPHRASE -out "${BACKUP_FILE}"

FILESIZE=$(stat -c%s "${BACKUP_FILE}" 2>/dev/null || stat -f%z "${BACKUP_FILE}")
echo "[BACKUP] Encrypted dump completed. File size: ${FILESIZE} bytes."

echo "[BACKUP] Uploading to Cloudflare R2 bucket: ${R2_BUCKET}..."
aws --endpoint-url "${R2_ENDPOINT}" s3 cp "${BACKUP_FILE}" "s3://${R2_BUCKET}/backups/$(basename "${BACKUP_FILE}")"

echo "[BACKUP] Upload completed successfully. Cleaning up local file..."
rm -f "${BACKUP_FILE}"

echo "[BACKUP] Backup procedure finished successfully at $(date -u +%Y%m%d_%H%M%SZ)."
