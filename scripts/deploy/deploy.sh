#!/usr/bin/env bash
# Deploy pinned image digests with a pre-deploy backup and automatic rollback (P0-05).
#
#   deploy.sh <api-digest> [web-digest]   deploy; web keeps the running digest when omitted
#   deploy.sh rollback                    redeploy the previous recorded digests
#
# A digest is sha256:<64 hex>, printed in the summary of the "Release - Publish" workflow run.
# Steps: backup -> pull -> up (migrate runs first) -> wait for a healthy api -> record digests.
# If pull, migrate, start or health fails, the previous digests are redeployed (migrations are
# additive, so the previous image still works on the migrated schema). Runbook: docs/setup/deploy.md.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${DEPLOY_COMPOSE_FILE:-${REPO_DIR}/deploy/docker/docker-compose.prod.yml}"
STATE_DIR="${DEPLOY_STATE_DIR:-/var/lib/mycaffe/deploy}"
HEALTH_TIMEOUT_S="${DEPLOY_HEALTH_TIMEOUT_S:-180}"
BACKUP_ENV_FILE="${BACKUP_ENV_FILE:-/etc/mycaffe/backup.env}"
DIGEST_RE='^sha256:[0-9a-f]{64}$'

log() { echo "[DEPLOY] $*"; }
die() { echo "[DEPLOY ERROR] $*" >&2; exit 1; }
compose() { docker compose -f "${COMPOSE_FILE}" "$@"; }

read_state() { # state file -> "api web" (empty when missing)
  [[ -f "$1" ]] || { echo " "; return; }
  (. "$1"; echo "${API_IMAGE_DIGEST:-} ${WEB_IMAGE_DIGEST:-}")
}

history() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $*" >> "${STATE_DIR}/history.log"; }

mkdir -p "${STATE_DIR}"
exec 9>"${STATE_DIR}/.lock"
flock -n 9 || die "Another deploy is running."

read -r CUR_API CUR_WEB <<< "$(read_state "${STATE_DIR}/current.env")"
if [[ "${1:-}" == "rollback" ]]; then
  ACTION=rollback
  read -r NEW_API NEW_WEB <<< "$(read_state "${STATE_DIR}/previous.env")"
  [[ -n "${NEW_API}" ]] || die "No previous deployment recorded in ${STATE_DIR}/previous.env."
else
  ACTION=deploy
  NEW_API="${1:-}"
  NEW_WEB="${2:-${CUR_WEB}}"
fi
[[ "${NEW_API}" =~ ${DIGEST_RE} ]] || die "Usage: deploy.sh <api sha256:digest> [web sha256:digest] | rollback (got api='${NEW_API}')."
[[ "${NEW_WEB}" =~ ${DIGEST_RE} ]] || die "A web digest is required on the first deploy (got web='${NEW_WEB}')."

wait_healthy() { # expected api digest
  local deadline=$(( SECONDS + HEALTH_TIMEOUT_S )) id image status
  while (( SECONDS < deadline )); do
    id="$(compose ps -q api 2>/dev/null || true)"
    if [[ -n "${id}" ]]; then
      image="$(docker inspect -f '{{.Config.Image}}' "${id}")"
      status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "${id}")"
      [[ "${image}" == *"@$1" && "${status}" == "healthy" ]] && return 0
    fi
    sleep 3
  done
  log "api did not become healthy within ${HEALTH_TIMEOUT_S}s (last status: ${status:-none})."
  return 1
}

apply() { # api digest, web digest
  export API_IMAGE_DIGEST="$1" WEB_IMAGE_DIGEST="$2"
  log "Pulling api@$1 web@$2 ..."
  compose pull --quiet || return 1
  log "Starting (migrate runs before api) ..."
  compose up -d || return 1
  wait_healthy "$1"
}

if [[ "${DEPLOY_SKIP_BACKUP:-false}" == "true" ]]; then
  log "WARNING: pre-deploy backup skipped (DEPLOY_SKIP_BACKUP=true)."
else
  log "Pre-deploy backup ..."
  # Same lock as the hourly cron, so the two never overlap.
  flock /tmp/mycaffe-backup.lock bash -c 'set -a; . "$1"; set +a; exec "$2"' _ \
    "${BACKUP_ENV_FILE}" "${REPO_DIR}/scripts/backup/backup_to_r2.sh" || die "Backup failed; nothing was deployed."
fi

if apply "${NEW_API}" "${NEW_WEB}"; then
  if [[ -n "${CUR_API}" && "${CUR_API} ${CUR_WEB}" != "${NEW_API} ${NEW_WEB}" ]]; then
    cp "${STATE_DIR}/current.env" "${STATE_DIR}/previous.env"
  fi
  printf 'API_IMAGE_DIGEST=%s\nWEB_IMAGE_DIGEST=%s\n' "${NEW_API}" "${NEW_WEB}" > "${STATE_DIR}/current.env.tmp"
  mv "${STATE_DIR}/current.env.tmp" "${STATE_DIR}/current.env"
  history "${ACTION} ok api=${NEW_API} web=${NEW_WEB}"
  log "Deployed api@${NEW_API} web@${NEW_WEB}."
  exit 0
fi

log "Deploy of api@${NEW_API} failed."
if [[ -z "${CUR_API}" ]]; then
  history "${ACTION} FAILED api=${NEW_API} web=${NEW_WEB}; no previous deployment to roll back to"
  die "First deploy failed; there is no previous deployment to roll back to."
fi
log "Rolling back to api@${CUR_API} web@${CUR_WEB} ..."
if apply "${CUR_API}" "${CUR_WEB}"; then
  history "${ACTION} FAILED api=${NEW_API} web=${NEW_WEB}; rolled back to api=${CUR_API} web=${CUR_WEB}"
  die "Rolled back to the previous deployment."
fi
history "${ACTION} FAILED api=${NEW_API}; ROLLBACK FAILED to api=${CUR_API}"
echo "[DEPLOY ERROR] Rollback failed too. See docs/setup/deploy.md (manual recovery)." >&2
exit 2
