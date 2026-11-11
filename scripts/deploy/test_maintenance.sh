#!/usr/bin/env bash
# Exercises the nginx maintenance switch (N3) against nginx.prod.conf with stub api/web upstreams.
# Usage: scripts/deploy/test_maintenance.sh (needs Docker). Exits non-zero on the first wrong answer.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NET="cassy-maint-test-$$"
PAGES="$(mktemp -d)"
cp "${ROOT}"/deploy/nginx/maintenance/_maintenance.* "${PAGES}/"
chmod 755 "${PAGES}"; chmod 644 "${PAGES}"/*

cleanup() {
  docker rm -f "${NET}-nginx" "${NET}-api" "${NET}-web" >/dev/null 2>&1 || true
  docker network rm "${NET}" >/dev/null 2>&1 || true
  rm -rf "${PAGES}"
}
trap cleanup EXIT

docker network create "${NET}" >/dev/null
STUB='server { listen 8080; listen 3000; location / { default_type text/plain; return 200 "upstream"; } }'
for s in api web; do
  docker run -d --name "${NET}-${s}" --network "${NET}" --network-alias "${s}" nginx:1.27-alpine \
    sh -c "echo '${STUB}' > /etc/nginx/conf.d/default.conf && nginx -g 'daemon off;'" >/dev/null
done
docker run -d --name "${NET}-nginx" --network "${NET}" \
  -v "${ROOT}/deploy/nginx/nginx.prod.conf:/etc/nginx/nginx.conf:ro" \
  -v "${PAGES}:/etc/nginx/maintenance:ro" nginx:1.27-alpine >/dev/null
sleep 2

fail=0
# expect <path> <status> <body substring> [Retry-After]
expect() {
  local out code
  out="$(docker run --rm --network "${NET}" curlimages/curl:8.11.1 -s -i "http://${NET}-nginx$1" | tr -d '\r')"
  code="$(echo "${out}" | head -1 | awk '{print $2}')"
  if [[ "${code}" != "$2" ]] || ! grep -qF -- "$3" <<<"${out}" || { [[ -n "${4:-}" ]] && ! grep -qi "^Retry-After: $4" <<<"${out}"; }; then
    echo "FAIL $1: want $2 '$3' ${4:+Retry-After $4}, got ${code}"; fail=1
  else
    echo "ok   $1 -> ${code}"
  fi
}

echo "== switch off"
expect / 200 upstream
expect /api/v1/orders 200 upstream

touch "${PAGES}/maintenance.on"
echo "== switch on"
expect /order/abc 503 "pemeliharaan singkat" 120
expect /api/v1/public/qr/x 503 '"code":"MAINTENANCE"' 120
expect /ws 503 '"code":"MAINTENANCE"' 120
expect /api/v1/payments/midtrans/webhook 200 upstream
expect /api/v1/payments/midtrans/webhook/central-cafe 200 upstream
expect /health/ready 200 upstream
expect /_maintenance.html 404 "Not Found"

rm "${PAGES}/maintenance.on"
echo "== switch off again"
expect / 200 upstream

exit "${fail}"
