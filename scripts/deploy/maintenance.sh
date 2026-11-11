#!/usr/bin/env bash
# Usage: maintenance.sh on|off|status
# Nginx checks the switch file on every request: guests get a static 503 page, POS/KDS get 503 JSON
# (code MAINTENANCE), and Midtrans webhooks and /health keep passing through. No reload needed.
set -euo pipefail
SWITCH="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/deploy/nginx/maintenance/maintenance.on"

case "${1:-status}" in
  on) date -u +%Y-%m-%dT%H:%M:%SZ > "${SWITCH}"; echo "[MAINTENANCE] on since $(cat "${SWITCH}")" ;;
  off) rm -f "${SWITCH}"; echo "[MAINTENANCE] off" ;;
  status) [[ -f "${SWITCH}" ]] && echo "[MAINTENANCE] on since $(cat "${SWITCH}")" || echo "[MAINTENANCE] off" ;;
  *) echo "Usage: $0 on|off|status" >&2; exit 2 ;;
esac
