#!/usr/bin/env bash
# Format a BI-SNAP partnerServiceId per Midtrans VA spec.
#
# Rule (per current Midtrans BI-SNAP docs and verified against real merchant
# code paths): the field must be exactly 8 characters in the create-VA
# payload. Numeric service ids shorter than 8 digits must be left-padded
# with spaces to 8 characters.
#
# Examples:
#   format_partner_service_id.sh 12345        -> "   12345"
#   format_partner_service_id.sh 12345678     -> "12345678"
#   format_partner_service_id.sh "  12345  "  -> "   12345"  (trims, repads)
#   format_partner_service_id.sh abc          -> error (non-numeric)
#
# The output preserves leading spaces verbatim and ends with a newline.
# For embedding in JSON, capture and pass through your JSON encoder so the
# spaces survive serialization.
#
# Exit codes: 0 ok, 2 bad input.

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <partner-service-id>" >&2
  exit 2
fi

# Trim surrounding whitespace before validation.
INPUT="$1"
TRIMMED="${INPUT#"${INPUT%%[![:space:]]*}"}"
TRIMMED="${TRIMMED%"${TRIMMED##*[![:space:]]}"}"

if [[ -z "$TRIMMED" ]]; then
  echo "partnerServiceId is empty" >&2
  exit 2
fi

# 1-8 digit numeric → left-pad with spaces to 8.
if [[ "$TRIMMED" =~ ^[0-9]{1,8}$ ]]; then
  printf '%8s\n' "$TRIMMED"
  exit 0
fi

# Already 8 chars and might contain spaces (legitimate left-padded form).
if [[ ${#INPUT} -eq 8 ]]; then
  printf '%s\n' "$INPUT"
  exit 0
fi

echo "partnerServiceId must be 1-8 digits or an 8-character left-padded value" >&2
echo "  received: \"${INPUT}\" (length=${#INPUT})" >&2
exit 2
