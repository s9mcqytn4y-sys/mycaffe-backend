#!/usr/bin/env python3
"""Compute the BI-SNAP access-token X-SIGNATURE header.

Algorithm (per Midtrans BI-SNAP docs):
    stringToSign = f"{clientId}|{timestamp}"
    signature    = base64(RSA-SHA256(privateKey, stringToSign))

This is the asymmetric signature used only on POST /v1.0/access-token/b2b.
It is NOT used for any other BI-SNAP request — those use the HMAC
transactional signature (see sign_bisnap_transaction.py).

PEM normalization handles three common input shapes:
    1. Plain PEM with real newlines.
    2. PEM with literal `\\n` sequences (common when stored in env vars).
    3. Base64-encoded PEM blob (one-line, no header).

Inputs:
    --client-id CLIENT_ID | env BISNAP_CLIENT_ID
    --timestamp ISO8601 (optional; defaults to current Asia/Jakarta)
    --private-key-file PATH | env BISNAP_PRIVATE_KEY (raw or \\n-escaped or base64)

Stdout: the base64 signature, suitable for X-SIGNATURE on the access-token call.

Requires the `cryptography` package, which ships with most Python
distributions; if it is missing, install with `pip install cryptography` in
the merchant's project venv. We do not install it system-wide.
"""

from __future__ import annotations

import argparse
import base64
import os
import re
import sys
from datetime import datetime, timedelta, timezone

JAKARTA = timezone(timedelta(hours=7))


def jakarta_now() -> str:
    return datetime.now(JAKARTA).strftime("%Y-%m-%dT%H:%M:%S+07:00")


def normalize_pem(value: str) -> str:
    """Accept PEM, escaped-newline PEM, or base64-encoded PEM. Return real PEM.

    Mirrors normalization seen in merchant production code so the script
    accepts whatever shape the merchant has stored in their secret manager.
    """
    text = value.strip().replace("\\n", "\n")
    if "BEGIN" in text:
        match = re.search(
            r"-----BEGIN ([^-]+)-----\s*([A-Za-z0-9+/=\s]+?)\s*-----END \1-----",
            text,
        )
        if match:
            label, body = match.group(1), match.group(2)
            wrapped = "\n".join(re.findall(".{1,64}", body.replace("\n", "").replace(" ", "")))
            return f"-----BEGIN {label}-----\n{wrapped}\n-----END {label}-----"
        return text
    # Treat as base64-encoded PEM.
    try:
        decoded = base64.b64decode(text).decode("utf-8")
    except Exception as exc:
        raise ValueError(f"key is not PEM and not base64 PEM: {exc}") from exc
    return decoded.replace("\\n", "\n")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--client-id", default=os.environ.get("BISNAP_CLIENT_ID", ""))
    parser.add_argument("--timestamp", default=None)
    parser.add_argument("--private-key-file", default=None)
    args = parser.parse_args(argv)

    if not args.client_id:
        print("BISNAP_CLIENT_ID env or --client-id required", file=sys.stderr)
        return 2

    if args.private_key_file:
        try:
            with open(args.private_key_file, "r", encoding="utf-8") as fh:
                raw = fh.read()
        except FileNotFoundError:
            print(f"private key file not found: {args.private_key_file}", file=sys.stderr)
            return 2
        except OSError as exc:
            print(f"cannot read private key file {args.private_key_file}: {exc}", file=sys.stderr)
            return 2
    elif os.environ.get("BISNAP_PRIVATE_KEY"):
        raw = os.environ["BISNAP_PRIVATE_KEY"]
    else:
        print("--private-key-file or BISNAP_PRIVATE_KEY env required", file=sys.stderr)
        return 2

    try:
        pem = normalize_pem(raw)
    except ValueError as exc:
        print(f"invalid private key input: {exc}", file=sys.stderr)
        return 2

    try:
        from cryptography.hazmat.primitives import hashes, serialization
        from cryptography.hazmat.primitives.asymmetric import padding
    except ImportError:
        print("cryptography package missing. Install with: pip install cryptography", file=sys.stderr)
        return 2

    try:
        private_key = serialization.load_pem_private_key(pem.encode("utf-8"), password=None)
    except (TypeError, ValueError) as exc:
        print(f"invalid private key PEM: {exc}", file=sys.stderr)
        return 2
    timestamp = args.timestamp or jakarta_now()
    string_to_sign = f"{args.client_id}|{timestamp}".encode("utf-8")

    signature = private_key.sign(string_to_sign, padding.PKCS1v15(), hashes.SHA256())
    print(base64.b64encode(signature).decode("ascii"))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
