#!/usr/bin/env bash
# ==============================================================================
# MyCaffe POS Platform - Initial Let's Encrypt Certificate Issuance Script
# Usage:
#   CERTBOT_EMAIL="ops@yourdomain.com" CERTBOT_DOMAINS="pos.yourdomain.com,api.yourdomain.com" \
#   ./scripts/deploy/init-letsencrypt.sh
# ==============================================================================
set -euo pipefail

COMPOSE_FILE="${COMPOSE_FILE:-deploy/docker/docker-compose.prod.yml}"
ENV_FILE="${ENV_FILE:-.env}"
CERT_NAME="mycaffe"

if [[ -z "${CERTBOT_EMAIL:-}" ]] || [[ -z "${CERTBOT_DOMAINS:-}" ]]; then
  echo "[ERROR] CERTBOT_EMAIL and CERTBOT_DOMAINS (comma-separated) must be set." >&2
  exit 1
fi

echo "[1/4] Starting Nginx with temporary TLS bootstrap certificate..."
docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" up -d nginx

echo "[2/4] Removing temporary bootstrap certificate before requesting Let's Encrypt certificate..."
docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" run --rm --entrypoint "\
  rm -rf /etc/letsencrypt/live/${CERT_NAME} /etc/letsencrypt/archive/${CERT_NAME} /etc/letsencrypt/renewal/${CERT_NAME}.conf" certbot

DOMAIN_ARGS=""
IFS=',' read -ra DOMAINS <<< "$CERTBOT_DOMAINS"
for d in "${DOMAINS[@]}"; do
  trimmed="$(echo "$d" | xargs)"
  if [[ -n "$trimmed" ]]; then
    DOMAIN_ARGS="${DOMAIN_ARGS} -d ${trimmed}"
  fi
done

STAGING_ARG=""
if [[ "${CERTBOT_STAGING:-0}" == "1" ]]; then
  STAGING_ARG="--staging"
fi

echo "[3/4] Requesting Let's Encrypt certificate for: ${CERTBOT_DOMAINS}..."
# shellcheck disable=SC2086
docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" run --rm --entrypoint "\
  certbot certonly --webroot -w /var/www/certbot \
    ${STAGING_ARG} \
    --cert-name ${CERT_NAME} \
    --email ${CERTBOT_EMAIL} \
    --rsa-key-size 4096 \
    --agree-tos \
    --non-interactive \
    --force-renewal \
    ${DOMAIN_ARGS}" certbot

echo "[4/4] Reloading Nginx with live Let's Encrypt certificate..."
docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" exec nginx nginx -s reload
echo "[SUCCESS] Let's Encrypt TLS certificate installed and active."
