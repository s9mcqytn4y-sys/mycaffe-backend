#!/usr/bin/env python3
"""End-to-end BI-SNAP signing dry-run.

Given a method, path, request body, and BI-SNAP credentials, emit:
    - the body hash (lowercase-hex SHA-256)
    - the canonical string-to-sign
    - the X-TIMESTAMP value
    - the transactional X-SIGNATURE
    - the headers that would be sent (with secrets redacted)

No network is performed. Use this to debug "signature error" responses in
sandbox without re-running the failing request.

Inputs:
    --method, --path                     transactional request
    --body-file PATH                     JSON body (or - for stdin)
    --access-token TOKEN | env BISNAP_ACCESS_TOKEN
    --client-secret SECRET | env BISNAP_CLIENT_SECRET
    --partner-id PID    | env BISNAP_PARTNER_ID
    --channel-id CID    | env BISNAP_CHANNEL_ID
    --external-id EID   | env BISNAP_EXTERNAL_ID  (default: epoch-ms)
    --customer-token T  | env BISNAP_CUSTOMER_AUTHORIZATION_TOKEN
                          if set, prints Authorization-Customer header

Output goes to stderr (human-readable). Stdout stays empty so this is safe
to chain. Exit codes: 0 ok, 2 bad input.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import os
import sys
import time
from datetime import datetime, timedelta, timezone

JAKARTA = timezone(timedelta(hours=7))


def jakarta_now() -> str:
    return datetime.now(JAKARTA).strftime("%Y-%m-%dT%H:%M:%S+07:00")


def redact(value: str, keep: int = 6) -> str:
    if not value:
        return ""
    if len(value) <= keep:
        return "*" * len(value)
    return value[:keep] + "*" * (len(value) - keep)


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--method", required=True)
    parser.add_argument("--path", required=True)
    parser.add_argument("--body-file", required=True)
    parser.add_argument("--access-token", default=os.environ.get("BISNAP_ACCESS_TOKEN", ""))
    parser.add_argument("--client-secret", default=os.environ.get("BISNAP_CLIENT_SECRET", ""))
    parser.add_argument("--partner-id", default=os.environ.get("BISNAP_PARTNER_ID", ""))
    parser.add_argument("--channel-id", default=os.environ.get("BISNAP_CHANNEL_ID", ""))
    parser.add_argument("--external-id", default=os.environ.get("BISNAP_EXTERNAL_ID", ""))
    parser.add_argument(
        "--customer-token",
        default=os.environ.get("BISNAP_CUSTOMER_AUTHORIZATION_TOKEN", ""),
    )
    args = parser.parse_args(argv)

    missing = []
    for name, value in [
        ("access-token", args.access_token),
        ("client-secret", args.client_secret),
        ("partner-id", args.partner_id),
        ("channel-id", args.channel_id),
    ]:
        if not value:
            missing.append(name)
    if missing:
        print("missing required inputs: " + ", ".join(missing), file=sys.stderr)
        return 2

    external_id = args.external_id or str(int(time.time() * 1000))

    try:
        if args.body_file == "-":
            body_bytes = sys.stdin.buffer.read()
        else:
            with open(args.body_file, "rb") as fh:
                body_bytes = fh.read()
    except FileNotFoundError:
        print(f"body file not found: {args.body_file}", file=sys.stderr)
        return 2
    except OSError as exc:
        print(f"cannot read body file {args.body_file}: {exc}", file=sys.stderr)
        return 2

    body_hash = hashlib.sha256(body_bytes).hexdigest().lower()
    timestamp = jakarta_now()
    string_to_sign = f"{args.method.upper()}:{args.path}:{args.access_token}:{body_hash}:{timestamp}"
    signature = base64.b64encode(
        hmac.new(
            args.client_secret.encode("utf-8"),
            string_to_sign.encode("utf-8"),
            hashlib.sha512,
        ).digest()
    ).decode("ascii")

    print("=== BI-SNAP signing dry-run ===", file=sys.stderr)
    print(f"method:        {args.method.upper()}", file=sys.stderr)
    print(f"endpoint_path: {args.path}", file=sys.stderr)
    print(f"timestamp:     {timestamp}", file=sys.stderr)
    print(f"external_id:   {external_id}", file=sys.stderr)
    print(f"body_size:     {len(body_bytes)} bytes", file=sys.stderr)
    print(f"body_hash:     {body_hash}", file=sys.stderr)
    print(file=sys.stderr)
    print("string_to_sign (redacted):", file=sys.stderr)
    print(
        f"  {args.method.upper()}:{args.path}:{redact(args.access_token, 8)}:{body_hash}:{timestamp}",
        file=sys.stderr,
    )
    print(file=sys.stderr)
    print("would-send headers:", file=sys.stderr)
    print(f"  Authorization:  Bearer {redact(args.access_token, 8)}", file=sys.stderr)
    print(f"  X-PARTNER-ID:   {args.partner_id}", file=sys.stderr)
    print(f"  X-EXTERNAL-ID:  {external_id}", file=sys.stderr)
    print(f"  X-TIMESTAMP:    {timestamp}", file=sys.stderr)
    print(f"  X-SIGNATURE:    {signature[:24]}... (truncated)", file=sys.stderr)
    print(f"  CHANNEL-ID:     {args.channel_id}", file=sys.stderr)
    if args.customer_token:
        print(
            f"  Authorization-Customer: Bearer {redact(args.customer_token, 8)}",
            file=sys.stderr,
        )
    print(file=sys.stderr)
    print(f"client_secret (redacted): {redact(args.client_secret, 4)}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
