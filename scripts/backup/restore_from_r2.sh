#!/usr/bin/env bash
# ==============================================================================
# MyCaffe POS Platform - Offsite Encrypted PostgreSQL 18 Restore Script (R2)
# Downloads GPG-encrypted backup + SHA-256 manifest from Cloudflare R2
# (mycaffe-backups), verifies checksum integrity, decrypts via file descriptor,
# and restores into PostgreSQL 18.
#
# Usage:
#   BACKUP_OBJECT="daily/backup_mycaffe_prod_20261028_190000Z.dump.gpg" ./scripts/backup/restore_from_r2.sh
#   BACKUP_OBJECT=latest ./scripts/backup/restore_from_r2.sh   # newest object in hourly/
#
# Target: set DB_CONTAINER to restore through `docker exec` (pg_restore 18 inside the
# container, local socket, no password). Otherwise DB_HOST/DB_PORT/DB_MIGRATOR_PASSWORD
# are used with a host pg_restore, which must be version 18.
# The target database must already have the mycaffe_* roles (deploy/postgres/00-init-roles.sh):
# GRANTs to mycaffe_app are restored from the dump, so the API works right after a restore.
# ==============================================================================
set -euo pipefail
umask 077

readonly R2_BACKUP_PREFIX="${R2_BACKUP_PREFIX:-backups}"

REQUIRED_VARS=(
  "DB_NAME"
  "DB_MIGRATOR_USER"
  "BACKUP_ENCRYPTION_PASSPHRASE"
  "R2_ENDPOINT"
  "R2_BACKUP_BUCKET"
  "AWS_ACCESS_KEY_ID"
  "AWS_SECRET_ACCESS_KEY"
  "BACKUP_OBJECT"
)
if [[ -z "${DB_CONTAINER:-}" ]]; then
  REQUIRED_VARS+=("DB_HOST" "DB_PORT" "DB_MIGRATOR_PASSWORD")
fi

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

readonly BASE="s3://${R2_BACKUP_BUCKET}/${R2_BACKUP_PREFIX}"
s3() { aws --endpoint-url "${R2_ENDPOINT}" s3 "$@"; }

OBJECT="${BACKUP_OBJECT}"
if [[ "${OBJECT}" == "latest" ]]; then
  # Names embed a sortable UTC timestamp, so the last name is the newest backup.
  OBJECT="hourly/$(s3 ls "${BASE}/hourly/" | awk '{print $4}' | grep '\.dump\.gpg$' | sort | tail -n 1)"
  if [[ "${OBJECT}" == "hourly/" ]]; then
    echo "[RESTORE ERROR] No backup found under ${BASE}/hourly/." >&2
    exit 1
  fi
fi

readonly WORK_DIR="$(mktemp -d -t mycaffe-restore-XXXXXXXXXX)"
readonly ENC_FILE="${WORK_DIR}/$(basename "${OBJECT}")"
readonly SHA_FILE="${ENC_FILE}.sha256"
readonly DUMP_FILE="${WORK_DIR}/restored.dump"

cleanup() {
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

echo "[RESTORE] Downloading ${OBJECT} and checksum from ${BASE}..."
s3 cp --only-show-errors "${BASE}/${OBJECT}" "${ENC_FILE}"
s3 cp --only-show-errors "${BASE}/${OBJECT}.sha256" "${SHA_FILE}"

echo "[RESTORE] Verifying SHA-256 integrity before decryption..."
(
  cd "${WORK_DIR}"
  sha256sum -c "$(basename "${SHA_FILE}")"
)

echo "[RESTORE] Decrypting backup archive with GPG..."
gpg --batch --yes --quiet --decrypt \
  --passphrase-fd 3 \
  --output "${DUMP_FILE}" \
  "${ENC_FILE}" 3<<< "${BACKUP_ENCRYPTION_PASSPHRASE}"

# Privileges are restored on purpose: dropping them (--no-privileges) leaves mycaffe_app
# without its GRANTs and the API fails with "permission denied" after a restore.
# Extensions are skipped: they belong to the superuser (00-init-roles.sh creates them), and
# --clean would try to drop them as mycaffe_migrator and abort the whole restore.
RESTORE_ARGS=(-U "${DB_MIGRATOR_USER}" -d "${DB_NAME}" --clean --if-exists --no-owner --single-transaction --exit-on-error)
LIST_FILTER="pg_restore -l \"\$1\" | grep -v ' EXTENSION ' > \"\$1.list\""

if [[ -n "${DB_CONTAINER:-}" ]]; then
  echo "[RESTORE] Restoring database ${DB_NAME} in container ${DB_CONTAINER} as ${DB_MIGRATOR_USER}..."
  readonly IN_CONTAINER="/tmp/mycaffe-restore-$$.dump"
  docker cp "${DUMP_FILE}" "${DB_CONTAINER}:${IN_CONTAINER}"
  restore_status=0
  docker exec "${DB_CONTAINER}" sh -c "${LIST_FILTER}" sh "${IN_CONTAINER}" \
    && docker exec "${DB_CONTAINER}" pg_restore "${RESTORE_ARGS[@]}" -L "${IN_CONTAINER}.list" "${IN_CONTAINER}" \
    || restore_status=$?
  docker exec "${DB_CONTAINER}" rm -f "${IN_CONTAINER}" "${IN_CONTAINER}.list"
  [[ "${restore_status}" -eq 0 ]] || exit "${restore_status}"
else
  echo "[RESTORE] Restoring database ${DB_NAME} on ${DB_HOST}:${DB_PORT} as ${DB_MIGRATOR_USER}..."
  sh -c "${LIST_FILTER}" sh "${DUMP_FILE}"
  PGPASSWORD="${DB_MIGRATOR_PASSWORD}" pg_restore -h "${DB_HOST}" -p "${DB_PORT}" "${RESTORE_ARGS[@]}" -L "${DUMP_FILE}.list" "${DUMP_FILE}"
fi

echo "[RESTORE SUCCESS] Database ${DB_NAME} restored and verified from ${OBJECT}."
