#!/usr/bin/env bash
# ==============================================================================
# MyCaffe POS Platform - Offsite Encrypted PostgreSQL 18 Restore Script (R2)
# Downloads GPG-encrypted backup + SHA-256 manifest from Cloudflare R2
# (mycaffe-backups), verifies checksum integrity, decrypts via file descriptor,
# and restores into PostgreSQL 18.
#
# Usage:
#   BACKUP_OBJECT="backup_mycaffe_prod_20261028_190000Z.dump.gpg" ./scripts/backup/restore_from_r2.sh
# ==============================================================================
set -euo pipefail
umask 077

readonly R2_BACKUP_PREFIX="${R2_BACKUP_PREFIX:-backups}"

readonly REQUIRED_VARS=(
  "DB_HOST"
  "DB_PORT"
  "DB_NAME"
  "DB_MIGRATOR_USER"
  "DB_MIGRATOR_PASSWORD"
  "BACKUP_ENCRYPTION_PASSPHRASE"
  "R2_ENDPOINT"
  "R2_BACKUP_BUCKET"
  "AWS_ACCESS_KEY_ID"
  "AWS_SECRET_ACCESS_KEY"
  "BACKUP_OBJECT"
)

for var in "${REQUIRED_VARS[@]}"; do
  if [[ -z "${!var:-}" ]]; then
    echo "[RESTORE ERROR] Required environment variable ${var} is not set." >&2
    exit 1
  fi
done

is_disposable_drill_db() {
  local name="$1"
  if [[ "${name}" =~ (test|temp|drill|recovery|disposable) ]]; then
    return 0
  fi
  return 1
}

if ! is_disposable_drill_db "${DB_NAME}" || [[ "${APP_ENV:-}" == "production" || "${APP_ENV:-}" == "staging" ]]; then
  if [[ "${CONFIRM_DESTRUCTIVE_RESTORE:-}" != "true" ]]; then
    echo "[RESTORE BLOCKED] Target database '${DB_NAME}' (APP_ENV=${APP_ENV:-unknown}) is protected against destructive restore." >&2
    echo "To proceed with overwriting this database, you must explicitly set CONFIRM_DESTRUCTIVE_RESTORE=true." >&2
    exit 1
  fi
fi

readonly WORK_DIR="$(mktemp -d -t mycaffe-restore-XXXXXXXXXX)"
readonly ENC_FILE="${WORK_DIR}/${BACKUP_OBJECT}"
readonly SHA_FILE="${ENC_FILE}.sha256"
readonly DUMP_FILE="${WORK_DIR}/restored.dump"

cleanup() {
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

echo "[RESTORE] Downloading ${BACKUP_OBJECT} and checksum from R2 (${R2_BACKUP_BUCKET}/${R2_BACKUP_PREFIX})..."
aws s3 cp "s3://${R2_BACKUP_BUCKET}/${R2_BACKUP_PREFIX}/${BACKUP_OBJECT}" "${ENC_FILE}" \
  --endpoint-url "${R2_ENDPOINT}" \
  --only-show-errors

aws s3 cp "s3://${R2_BACKUP_BUCKET}/${R2_BACKUP_PREFIX}/${BACKUP_OBJECT}.sha256" "${SHA_FILE}" \
  --endpoint-url "${R2_ENDPOINT}" \
  --only-show-errors

echo "[RESTORE] Verifying SHA-256 integrity before decryption..."
(
  cd "${WORK_DIR}"
  sha256sum -c "$(basename "${SHA_FILE}")"
)

echo "[RESTORE] Decrypting backup archive with GPG..."
gpg --batch --yes --decrypt \
  --passphrase-fd 3 \
  --output "${DUMP_FILE}" \
  "${ENC_FILE}" 3<<< "${BACKUP_ENCRYPTION_PASSPHRASE}"

echo "[RESTORE] Restoring database ${DB_NAME} on ${DB_HOST}:${DB_PORT} as ${DB_MIGRATOR_USER}..."
PGPASSWORD="${DB_MIGRATOR_PASSWORD}" pg_restore \
  -h "${DB_HOST}" \
  -p "${DB_PORT}" \
  -U "${DB_MIGRATOR_USER}" \
  -d "${DB_NAME}" \
  --clean \
  --if-exists \
  --no-owner \
  --no-privileges \
  --single-transaction \
  "${DUMP_FILE}"

echo "[RESTORE SUCCESS] Database ${DB_NAME} restored and verified from ${BACKUP_OBJECT}."
