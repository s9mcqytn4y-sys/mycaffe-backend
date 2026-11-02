#!/usr/bin/env bash
# docker compose for production with the digests deploy.sh recorded, e.g. `compose.sh logs api`
# or `compose.sh up -d alloy`. Deploy new images with deploy.sh, not with this wrapper.
set -euo pipefail
set -a
. "${DEPLOY_STATE_DIR:-/var/lib/mycaffe/deploy}/current.env"
set +a
exec docker compose -f "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/deploy/docker/docker-compose.prod.yml" "$@"
