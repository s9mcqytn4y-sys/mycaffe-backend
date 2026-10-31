#!/usr/bin/env python3
"""Compute the BI-SNAP transactional X-SIGNATURE header.

Algorithm (per Midtrans BI-SNAP docs):
    bodyHash     = lowercase-hex SHA-256 of the exact request body bytes
    stringToSign = f"{method}:{endpointPath}:{accessToken}:{bodyHash}:{timestamp}"
    signature    = base64(HMAC-SHA512(key=clientSecret, message=stringToSign))

Matches the transactional signing rule documented at
integrate-midtrans-payments/references/bisnap-core.md.

The byte string passed to --body-file must be exactly the byte string sent
on the wire. Serialize JSON once, sign that string, send that string.

Inputs:
    --method, --path                      transactional request method + path
    --body-file PATH                      JSON body file (use - for stdin)
    --timestamp ISO8601                   defaults to current Asia/Jakarta time
    --access-token TOKEN | env BISNAP_ACCESS_TOKEN
    --client-secret SECRET | env BISNAP_CLIENT_SECRET
    --verbose                             print string-to-sign with secrets
                                          redacted (for debugging)

Stdout: the base64 signature, suitable for X-SIGNATURE.

Exit codes: 0 ok, 2 bad input.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import os
import sys
from datetime import datetime, timedelta, timezone

JAKARTA = timezone(timedelta(hours=7))


def jakarta_now() -> str:
    """Asia/Jakarta ISO-8601 timestamp, second precision, +07:00 suffix.

    Mirrors the format Midtrans expects and matches production merchant
    implementations.
    """
    return datetime.now(JAKARTA).strftime("%Y-%m-%dT%H:%M:%S+07:00")


def redact(secret: str, keep: int = 4) -> str:
    if not secret:
        return ""
    if len(secret) <= keep:
        return "*" * len(secret)
    return secret[:keep] + "*" * (len(secret) - keep)


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--method", required=True, help="HTTP method, e.g. POST")
    parser.add_argument("--path", required=True, help="Endpoint path, no host or query")
    parser.add_argument("--body-file", required=True, help="JSON body path or - for stdin")
    parser.add_argument("--timestamp", default=None, help="Override timestamp; default: now in Asia/Jakarta")
    parser.add_argument("--access-token", default=os.environ.get("BISNAP_ACCESS_TOKEN", ""))
    parser.add_argument("--client-secret", default=os.environ.get("BISNAP_CLIENT_SECRET", ""))
    parser.add_argument("--verbose", action="store_true")
    args = parser.parse_args(argv)

    if not args.access_token:
        print("BISNAP_ACCESS_TOKEN env or --access-token required", file=sys.stderr)
        return 2
    if not args.client_secret:
        print("BISNAP_CLIENT_SECRET env or --client-secret required", file=sys.stderr)
        return 2

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
    timestamp = args.timestamp or jakarta_now()
    string_to_sign = f"{args.method.upper()}:{args.path}:{args.access_token}:{body_hash}:{timestamp}"

    sig = base64.b64encode(
        hmac.new(args.client_secret.encode("utf-8"), string_to_sign.encode("utf-8"), hashlib.sha512).digest()
    ).decode("ascii")

    if args.verbose:
        # Redact the access token and client secret in the printed canonical
        # string. Do not print to stdout — keep stdout machine-parseable.
        redacted = (
            f"{args.method.upper()}:{args.path}:{redact(args.access_token, 8)}:"
            f"{body_hash}:{timestamp}"
        )
        print("string_to_sign (redacted):", redacted, file=sys.stderr)
        print("body_hash:", body_hash, file=sys.stderr)
        print("timestamp:", timestamp, file=sys.stderr)
        print("client_secret:", redact(args.client_secret, 4), file=sys.stderr)

    print(sig)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
