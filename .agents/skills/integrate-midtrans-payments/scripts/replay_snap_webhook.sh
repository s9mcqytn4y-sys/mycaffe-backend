#!/usr/bin/env bash
# Replay a Snap webhook against a merchant's local handler.
#
# Builds a Snap notification payload, signs it with MIDTRANS_SERVER_KEY using
# the SHA-512 rule, and POSTs it to a target URL. Used to exercise the
# merchant's webhook handler in isolation (idempotency, signature checks,
# state transitions) without driving a real payment.
#
# Safety (two independent axes):
#   - Server key: refuses if MIDTRANS_SERVER_KEY does not start with "SB-".
#     Use --allow-production only with explicit merchant approval.
#   - Target URL: refuses non-localhost targets by default. localhost,
#     127.0.0.1, ::1, 0.0.0.0, and *.local are allowed. Anything else
#     (staging, dev cluster, ngrok tunnel, real merchant URL) requires
#     --remote-target. Together with --allow-production this means an
#     accidental "replay production-key notification against a remote URL"
#     needs two opt-in flags, not one.
#   - --dry-run does not POST and skips target URL safety checks. Server-key
#     safety still applies because dry-run prints a signed payload.
#   - Server key never echoed.
#
# Inputs (env or args):
#   MIDTRANS_SERVER_KEY                  required (sandbox key)
#   --target-url URL                     where to POST the notification
#   --order-id ID                        notification order_id
#   --status-code 200                    notification status_code (default 200)
#   --gross-amount "10000.00"            byte-identical to provider format
#   --transaction-status settlement      transaction_status field
#   --fraud-status accept                fraud_status (default accept)
#   --payment-type credit_card           payment_type (default credit_card)
#   --fixture FILE                       load JSON template, override above
#   --dry-run                            print only, do not POST
#   --allow-production                   bypass server-key sandbox-only safety
#   --remote-target                      allow non-localhost target URL
#
# Exit codes: 0 = posted (or printed), 1 = HTTP non-2xx, 2 = bad input.

set -euo pipefail

usage() {
  sed -n '2,35p' "$0"
  exit 2
}

ALLOW_PRODUCTION=0
REMOTE_TARGET=0
DRY_RUN=0
TARGET_URL=""
ORDER_ID=""
STATUS_CODE="200"
GROSS_AMOUNT=""
TRANSACTION_STATUS="settlement"
FRAUD_STATUS="accept"
PAYMENT_TYPE="credit_card"
FIXTURE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target-url) TARGET_URL="$2"; shift 2;;
    --order-id) ORDER_ID="$2"; shift 2;;
    --status-code) STATUS_CODE="$2"; shift 2;;
    --gross-amount) GROSS_AMOUNT="$2"; shift 2;;
    --transaction-status) TRANSACTION_STATUS="$2"; shift 2;;
    --fraud-status) FRAUD_STATUS="$2"; shift 2;;
    --payment-type) PAYMENT_TYPE="$2"; shift 2;;
    --fixture) FIXTURE="$2"; shift 2;;
    --dry-run) DRY_RUN=1; shift;;
    --allow-production) ALLOW_PRODUCTION=1; shift;;
    --remote-target) REMOTE_TARGET=1; shift;;
    -h|--help) usage;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

if [[ -z "${MIDTRANS_SERVER_KEY:-}" ]]; then
  echo "MIDTRANS_SERVER_KEY env var is required" >&2
  exit 2
fi

if [[ "$MIDTRANS_SERVER_KEY" != SB-* && "$ALLOW_PRODUCTION" != 1 ]]; then
  echo "MIDTRANS_SERVER_KEY does not look like a sandbox key (no SB- prefix)." >&2
  echo "Refusing to run. Pass --allow-production if this is intentional." >&2
  exit 2
fi

if [[ -n "$FIXTURE" ]]; then
  if [[ ! -f "$FIXTURE" ]]; then
    echo "fixture file not found: $FIXTURE" >&2
    exit 2
  fi
  # Single Python invocation, clean error on malformed JSON.
  PARSED="$(python3 - "$FIXTURE" <<'PY'
import json, sys
path = sys.argv[1]
try:
    payload = json.load(open(path))
except json.JSONDecodeError as exc:
    print(f"fixture is not valid JSON: {exc.msg} at line {exc.lineno} col {exc.colno}", file=sys.stderr)
    sys.exit(2)
except OSError as exc:
    print(f"cannot read fixture: {exc}", file=sys.stderr)
    sys.exit(2)
defaults = {
    "order_id": "",
    "status_code": "200",
    "gross_amount": "",
    "transaction_status": "settlement",
    "fraud_status": "accept",
    "payment_type": "credit_card",
}
for key, default in defaults.items():
    print(f"{key}\t{payload.get(key, default)}")
PY
  )" || exit 2
  ORDER_ID="${ORDER_ID:-$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="order_id"{print $2}')}"
  STATUS_CODE="$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="status_code"{print $2}')"
  GROSS_AMOUNT="${GROSS_AMOUNT:-$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="gross_amount"{print $2}')}"
  TRANSACTION_STATUS="$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="transaction_status"{print $2}')"
  FRAUD_STATUS="$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="fraud_status"{print $2}')"
  PAYMENT_TYPE="$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="payment_type"{print $2}')"
fi

for v in ORDER_ID GROSS_AMOUNT; do
  if [[ -z "${!v}" ]]; then
    echo "$v is required" >&2
    exit 2
  fi
done

if [[ "$DRY_RUN" != 1 && -z "$TARGET_URL" ]]; then
  echo "--target-url required (or use --dry-run)" >&2
  exit 2
fi

is_local_target() {
  python3 - "$1" <<'PY'
from urllib.parse import urlparse
import sys

parsed = urlparse(sys.argv[1])
host = (parsed.hostname or "").lower().rstrip(".")
if parsed.scheme not in {"http", "https"}:
    sys.exit(1)
if host in {"localhost", "127.0.0.1", "::1", "0.0.0.0"} or host.endswith(".local"):
    sys.exit(0)
sys.exit(1)
PY
}

if [[ "$DRY_RUN" != 1 && "$REMOTE_TARGET" != 1 ]]; then
  if ! is_local_target "$TARGET_URL"; then
    echo "target URL is not localhost/local. Refusing to POST without --remote-target." >&2
    echo "Allowed by default: localhost, 127.0.0.1, ::1, 0.0.0.0, and *.local." >&2
    exit 2
  fi
fi

SIGNATURE="$(printf '%s' "${ORDER_ID}${STATUS_CODE}${GROSS_AMOUNT}${MIDTRANS_SERVER_KEY}" \
  | openssl dgst -sha512 -hex \
  | awk '{print $NF}')"

PAYLOAD="$(python3 - "$ORDER_ID" "$STATUS_CODE" "$GROSS_AMOUNT" "$TRANSACTION_STATUS" "$FRAUD_STATUS" "$PAYMENT_TYPE" "$SIGNATURE" <<'PY'
import json, sys
order_id, status_code, gross_amount, txn, fraud, pay_type, sig = sys.argv[1:8]
print(json.dumps({
    "order_id": order_id,
    "status_code": status_code,
    "gross_amount": gross_amount,
    "transaction_status": txn,
    "fraud_status": fraud,
    "payment_type": pay_type,
    "signature_key": sig,
    "transaction_time": "2026-05-27 14:30:00",
}, separators=(",", ":")))
PY
)"

if [[ "$DRY_RUN" == 1 ]]; then
  echo "would POST to: ${TARGET_URL:-<none>}"
  echo "payload: $PAYLOAD"
  echo "signature: ${SIGNATURE:0:16}... (truncated)"
  exit 0
fi

RESPONSE_FILE="$(mktemp "${TMPDIR:-/tmp}/replay_snap_response.XXXXXX")"
trap 'rm -f "$RESPONSE_FILE"' EXIT

# -fsS: fail on HTTP error, silent except errors. -w prints final status.
HTTP_STATUS="$(curl -fsS -o "$RESPONSE_FILE" -w '%{http_code}' \
  -X POST "$TARGET_URL" \
  -H 'Content-Type: application/json' \
  -d "$PAYLOAD" || true)"

echo "HTTP $HTTP_STATUS"
if [[ -s "$RESPONSE_FILE" ]]; then
  echo "response:"
  cat "$RESPONSE_FILE"
fi

if [[ "$HTTP_STATUS" != 2* ]]; then
  exit 1
fi
