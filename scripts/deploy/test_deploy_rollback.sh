#!/usr/bin/env bash
# CI test for deploy.sh: deploy two good digests, prove a broken image and a missing digest roll back
# automatically, then prove `deploy.sh rollback`. Uses a throwaway registry on 127.0.0.1:5005.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REG=127.0.0.1:5005
export DEPLOY_COMPOSE_FILE="${HERE}/testdata/compose.deploy-test.yml"
export DEPLOY_STATE_DIR="$(mktemp -d)"
export DEPLOY_SKIP_BACKUP=true DEPLOY_HEALTH_TIMEOUT_S=20 COMPOSE_PROJECT_NAME=deploytest

cleanup() {
  docker compose -f "${DEPLOY_COMPOSE_FILE}" down -t 1 >/dev/null 2>&1 || true
  docker rm -f deploytest-registry >/dev/null 2>&1 || true
  rm -rf "${DEPLOY_STATE_DIR}"
}
trap cleanup EXIT

docker run -d --name deploytest-registry -p "${REG}:5000" registry:2.8.3 >/dev/null
for i in $(seq 1 30); do curl -sf "http://${REG}/v2/" >/dev/null && break; sleep 1; done

push() { # source image, repo:tag -> prints digest
  docker tag "$1" "${REG}/$2" && docker push -q "${REG}/$2" >/dev/null
  # Ask the registry: the local RepoDigests can name a multi-platform index that was not pushed.
  curl -sfI \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json' \
    -H 'Accept: application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json' \
    "http://${REG}/v2/${2%%:*}/manifests/${2#*:}" | tr -d '\r' | awk -F': ' 'tolower($1) == "docker-content-digest" {print $2}'
}
docker pull -q busybox:1.37.0 >/dev/null
docker pull -q busybox:1.36.1 >/dev/null
printf 'FROM busybox:1.37.0\nRUN touch /unhealthy\n' | docker build -q -t deploytest-broken - >/dev/null

GOOD1="$(push busybox:1.36.1 mycaffe-api:good1)"
GOOD2="$(push busybox:1.37.0 mycaffe-api:good2)"
BROKEN="$(push deploytest-broken mycaffe-api:broken)"
WEB="$(push busybox:1.37.0 mycaffe-web:good)"
MISSING="sha256:$(printf '0%.0s' $(seq 1 64))"

running_api() { docker inspect -f '{{.Config.Image}}' "$(docker compose -f "${DEPLOY_COMPOSE_FILE}" ps -q api)" | cut -d@ -f2; }
state() { grep "^$2=" "${DEPLOY_STATE_DIR}/$1.env" | cut -d= -f2; }
expect() { [[ "$2" == "$3" ]] || { echo "FAIL: $1: got '$2', want '$3'" >&2; exit 1; }; echo "ok: $1"; }

if "${HERE}/deploy.sh" not-a-digest; then echo "FAIL: invalid digest accepted" >&2; exit 1; fi
if "${HERE}/deploy.sh" "${GOOD1}"; then echo "FAIL: first deploy without web digest accepted" >&2; exit 1; fi
echo "ok: invalid arguments rejected"

"${HERE}/deploy.sh" "${GOOD1}" "${WEB}"
"${HERE}/deploy.sh" "${GOOD2}"
expect "second deploy runs good2" "$(running_api)" "${GOOD2}"
expect "previous is good1" "$(state previous API_IMAGE_DIGEST)" "${GOOD1}"

set +e; "${HERE}/deploy.sh" "${BROKEN}"; rc=$?; set -e
expect "unhealthy image exits 1" "${rc}" 1
expect "unhealthy image rolled back" "$(running_api)" "${GOOD2}"
expect "current still good2" "$(state current API_IMAGE_DIGEST)" "${GOOD2}"

set +e; "${HERE}/deploy.sh" "${MISSING}"; rc=$?; set -e
expect "missing digest exits 1" "${rc}" 1
expect "missing digest rolled back" "$(running_api)" "${GOOD2}"

"${HERE}/deploy.sh" rollback
expect "manual rollback runs good1" "$(running_api)" "${GOOD1}"
expect "current is good1" "$(state current API_IMAGE_DIGEST)" "${GOOD1}"
expect "web unchanged" "$(state current WEB_IMAGE_DIGEST)" "${WEB}"

grep -c 'FAILED.*rolled back' "${DEPLOY_STATE_DIR}/history.log" | grep -qx 2 || { echo "FAIL: history.log" >&2; cat "${DEPLOY_STATE_DIR}/history.log" >&2; exit 1; }
echo "PASS: deploy.sh digest deploy and automatic rollback"
