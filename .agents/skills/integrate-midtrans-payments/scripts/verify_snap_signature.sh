#!/usr/bin/env bash
# Verify a Midtrans Snap / Core API notification signature.
#
# Algorithm (classic Midtrans):
#   expected = SHA-512(order_id + status_code + gross_amount + serverKey)
#
# Matches the verification rule documented at
# integrate-midtrans-payments/references/snap-checkout.md and what merchant
# production handlers compute.
#
# CRITICAL: use the gross_amount value as it appears in the notification
# payload, byte for byte. Do not parse, round, or reformat it before hashing.
#
# Inputs (env or args):
#   ORDER_ID, STATUS_CODE, GROSS_AMOUNT, SIGNATURE
#   MIDTRANS_SERVER_KEY  (sandbox key only; this script refuses production-
#                         looking keys unless --allow-production is passed)
#
# Usage:
#   MIDTRANS_SERVER_KEY=SB-Mid-server-xxx \
#     ./verify_snap_signature.sh \
#       --order-id ORDER-123 --status-code 200 \
#       --gross-amount 10000.00 --signature <hex>
#
#   ./verify_snap_signature.sh --payload notification.json
#
# Exit codes: 0 = match, 1 = mismatch, 2 = bad input.

set -euo pipefail

usage() {
  sed -n '2,30p' "$0"
  exit 2
}

ALLOW_PRODUCTION=0
PAYLOAD_FILE=""
ORDER_ID="${ORDER_ID:-}"
STATUS_CODE="${STATUS_CODE:-}"
GROSS_AMOUNT="${GROSS_AMOUNT:-}"
SIGNATURE="${SIGNATURE:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --order-id) ORDER_ID="$2"; shift 2;;
    --status-code) STATUS_CODE="$2"; shift 2;;
    --gross-amount) GROSS_AMOUNT="$2"; shift 2;;
    --signature) SIGNATURE="$2"; shift 2;;
    --payload) PAYLOAD_FILE="$2"; shift 2;;
    --allow-production) ALLOW_PRODUCTION=1; shift;;
    -h|--help) usage;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

if [[ -z "${MIDTRANS_SERVER_KEY:-}" ]]; then
  echo "MIDTRANS_SERVER_KEY env var is required" >&2
  exit 2
fi

# Production-key heuristic. Sandbox keys begin with "SB-". Refuse production
# unless the operator opts in explicitly.
if [[ "$MIDTRANS_SERVER_KEY" != SB-* && "$ALLOW_PRODUCTION" != 1 ]]; then
  echo "MIDTRANS_SERVER_KEY does not look like a sandbox key (no SB- prefix)." >&2
  echo "Refusing to run. Pass --allow-production if this is intentional." >&2
  exit 2
fi

if [[ -n "$PAYLOAD_FILE" ]]; then
  if [[ ! -f "$PAYLOAD_FILE" ]]; then
    echo "payload file not found: $PAYLOAD_FILE" >&2
    exit 2
  fi
  # Single Python invocation: catches malformed JSON cleanly, emits four
  # tab-separated KEY\tVALUE lines for the shell to read. Avoids four
  # separate python invocations and four chances to traceback.
  PARSED="$(python3 - "$PAYLOAD_FILE" <<'PY'
import json, sys
path = sys.argv[1]
try:
    payload = json.load(open(path))
except json.JSONDecodeError as exc:
    print(f"payload is not valid JSON: {exc.msg} at line {exc.lineno} col {exc.colno}", file=sys.stderr)
    sys.exit(2)
except OSError as exc:
    print(f"cannot read payload: {exc}", file=sys.stderr)
    sys.exit(2)
for key in ("order_id", "status_code", "gross_amount", "signature_key"):
    print(f"{key}\t{payload.get(key, '')}")
PY
  )" || exit 2
  ORDER_ID="$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="order_id"{print $2}')"
  STATUS_CODE="$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="status_code"{print $2}')"
  GROSS_AMOUNT="$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="gross_amount"{print $2}')"
  SIGNATURE="$(printf '%s\n' "$PARSED" | awk -F'\t' '$1=="signature_key"{print $2}')"
fi

for v in ORDER_ID STATUS_CODE GROSS_AMOUNT SIGNATURE; do
  if [[ -z "${!v}" ]]; then
    echo "$v is required (via env, --flag, or --payload)" >&2
    exit 2
  fi
done

# printf without trailing newline; openssl reads from stdin.
EXPECTED="$(printf '%s' "${ORDER_ID}${STATUS_CODE}${GROSS_AMOUNT}${MIDTRANS_SERVER_KEY}" \
  | openssl dgst -sha512 -hex \
  | awk '{print $NF}')"

if [[ "$EXPECTED" == "$SIGNATURE" ]]; then
  echo "OK"
  exit 0
fi

# Mismatch report: never echo the server key or the full expected hash to
# avoid confusion in logs. Show enough to debug payload-shape issues.
echo "MISMATCH" >&2
echo "  order_id      = ${ORDER_ID}" >&2
echo "  status_code   = ${STATUS_CODE}" >&2
echo "  gross_amount  = ${GROSS_AMOUNT}" >&2
echo "  expected_pref = ${EXPECTED:0:16}..." >&2
echo "  received_pref = ${SIGNATURE:0:16}..." >&2
exit 1
