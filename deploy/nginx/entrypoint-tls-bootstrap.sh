#!/usr/bin/env sh
# ==============================================================================
# MyCaffe POS Platform - Nginx First-Certificate TLS Bootstrap Entrypoint
# Solves the first-boot chicken-and-egg problem on an empty Let's Encrypt volume:
# 1. If /etc/letsencrypt/live/mycaffe/fullchain.pem does not exist yet, generates
#    a temporary 1-day self-signed certificate so Nginx can boot and serve
#    HTTP ACME challenges on port 80 (/.well-known/acme-challenge/).
# 2. Starts a background reload loop so Certbot renewals are picked up automatically.
# ==============================================================================
set -eu

CERT_DIR="/etc/letsencrypt/live/mycaffe"
FULLCHAIN="${CERT_DIR}/fullchain.pem"
PRIVKEY="${CERT_DIR}/privkey.pem"

mkdir -p /var/www/certbot

if [ ! -f "$FULLCHAIN" ] || [ ! -f "$PRIVKEY" ]; then
  echo "[NGINX TLS BOOTSTRAP] No certificate found at ${CERT_DIR}; generating temporary bootstrap certificate..."
  if ! command -v openssl >/dev/null 2>&1; then
    apk add --no-cache openssl >/dev/null
  fi
  mkdir -p "$CERT_DIR"
  openssl req -x509 -nodes -newkey rsa:2048 -days 1 \
    -keyout "$PRIVKEY" \
    -out "$FULLCHAIN" \
    -subj "/CN=localhost" 2>/dev/null
  echo "[NGINX TLS BOOTSTRAP] Temporary self-signed certificate created at ${CERT_DIR}."
fi

# Periodic graceful reload every 6 hours to pick up renewed Let's Encrypt certificates
(
  while :; do
    sleep 6h
    nginx -s reload || true
  done
) &

exec nginx -g "daemon off;"
