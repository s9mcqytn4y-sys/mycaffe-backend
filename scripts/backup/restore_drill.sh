#!/usr/bin/env bash
# ==============================================================================
# MyCaffe POS Platform - Monthly Restore Drill
# Restores the newest (or BACKUP_OBJECT) R2 backup into a throwaway PostgreSQL 18
# container, proves the API role can read it, compares migrations and row counts
# with the live database, prints one RESULT line, then removes the container.
# Never touches the live database except for read-only counts as mycaffe_backup.
#
# Usage (on the VPS, same env as backup_to_r2.sh):
#   ./scripts/backup/restore_drill.sh >> /var/log/mycaffe/restore-drill.log 2>&1
# Optional: BACKUP_OBJECT (default latest), SOURCE_CONTAINER (default mycaffe-postgres),
#           SOURCE_DB (default DB_NAME or mycaffe_prod), PG_IMAGE (default postgres:18-alpine)
# ==============================================================================
set -euo pipefail
umask 077

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly INIT_SCRIPT="${SCRIPT_DIR}/../../deploy/postgres/00-init-roles.sh"
readonly PG_IMAGE="${PG_IMAGE:-postgres:18-alpine}"
readonly SOURCE_CONTAINER="${SOURCE_CONTAINER:-mycaffe-postgres}"
readonly SOURCE_DB="${SOURCE_DB:-${DB_NAME:-mycaffe_prod}}"
readonly DRILL_DB="mycaffe_drill"
readonly DRILL_CONTAINER="mycaffe-restore-drill-$$"
readonly STARTED_AT="$(date -u +%s)"

for tool in docker aws gpg sha256sum openssl; do
  command -v "${tool}" >/dev/null || { echo "[DRILL ERROR] ${tool} is required." >&2; exit 1; }
done

cleanup() {
  docker rm -f "${DRILL_CONTAINER}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "[DRILL] Starting throwaway ${PG_IMAGE} container ${DRILL_CONTAINER}..."
# docker create + cp instead of a bind mount: works the same on Linux hosts and Git Bash.
docker create --name "${DRILL_CONTAINER}" \
  -e POSTGRES_PASSWORD="$(openssl rand -hex 24)" \
  -e POSTGRES_DB="${DRILL_DB}" \
  -e DB_MIGRATOR_USER=mycaffe_migrator -e DB_MIGRATOR_PASSWORD="$(openssl rand -hex 24)" \
  -e DB_APP_USER=mycaffe_app -e DB_APP_PASSWORD="$(openssl rand -hex 24)" \
  "${PG_IMAGE}" >/dev/null
docker cp "${INIT_SCRIPT}" "${DRILL_CONTAINER}:/docker-entrypoint-initdb.d/00-init-roles.sh"
docker start "${DRILL_CONTAINER}" >/dev/null

for _ in $(seq 1 60); do
  if docker logs "${DRILL_CONTAINER}" 2>&1 | grep -q 'PostgreSQL init process complete' \
     && docker exec "${DRILL_CONTAINER}" pg_isready -q -U mycaffe_migrator -d "${DRILL_DB}"; then
    break
  fi
  sleep 1
done
docker exec "${DRILL_CONTAINER}" pg_isready -q -U mycaffe_migrator -d "${DRILL_DB}" \
  || { echo "[DRILL ERROR] Throwaway PostgreSQL did not become ready." >&2; exit 1; }

BACKUP_OBJECT="${BACKUP_OBJECT:-latest}" DB_CONTAINER="${DRILL_CONTAINER}" DB_NAME="${DRILL_DB}" \
  DB_MIGRATOR_USER=mycaffe_migrator APP_ENV=drill \
  "${SCRIPT_DIR}/restore_from_r2.sh"

q() { # container, user, db, sql
  docker exec "$1" psql -X -q -t -A -v ON_ERROR_STOP=1 -U "$2" -d "$3" -c "$4"
}

echo "[DRILL] Checking that mycaffe_app can read the restored schema (GRANTs restored)..."
q "${DRILL_CONTAINER}" mycaffe_app "${DRILL_DB}" "SELECT count(*) FROM orders" >/dev/null

COUNT_SQL="SELECT (SELECT count(*) FROM schema_migrations)||' '||(SELECT count(*) FROM stores)||' '||(SELECT count(*) FROM orders)||' '||(SELECT count(*) FROM payments)||' '||(SELECT count(*) FROM audit_logs)"
read -r R_MIG R_STORES R_ORDERS R_PAYMENTS R_AUDIT <<< "$(q "${DRILL_CONTAINER}" postgres "${DRILL_DB}" "${COUNT_SQL}")"

STATUS="PASS"
if docker inspect "${SOURCE_CONTAINER}" >/dev/null 2>&1; then
  read -r L_MIG L_STORES L_ORDERS L_PAYMENTS L_AUDIT <<< "$(q "${SOURCE_CONTAINER}" mycaffe_backup "${SOURCE_DB}" "${COUNT_SQL}")"
  echo "[DRILL] live     : migrations=${L_MIG} stores=${L_STORES} orders=${L_ORDERS} payments=${L_PAYMENTS} audit_logs=${L_AUDIT}"
  # The backup may be up to an hour old: fewer rows is expected, more rows or a schema mismatch is not.
  if [[ "${R_MIG}" != "${L_MIG}" ]] || (( R_STORES > L_STORES || R_ORDERS > L_ORDERS || R_PAYMENTS > L_PAYMENTS || R_AUDIT > L_AUDIT )); then
    STATUS="FAIL"
  fi
else
  echo "[DRILL] Live container ${SOURCE_CONTAINER} not found; skipping live comparison."
fi
echo "[DRILL] restored : migrations=${R_MIG} stores=${R_STORES} orders=${R_ORDERS} payments=${R_PAYMENTS} audit_logs=${R_AUDIT}"
if (( R_MIG == 0 )); then
  STATUS="FAIL"
fi

DURATION=$(( $(date -u +%s) - STARTED_AT ))
echo "RESULT ${STATUS} at=$(date -u +%Y-%m-%dT%H:%M:%SZ) object=${BACKUP_OBJECT:-latest} duration_s=${DURATION} migrations=${R_MIG} orders=${R_ORDERS}"
[[ "${STATUS}" == "PASS" ]]
