#!/usr/bin/env python3
"""Verify a BI-SNAP notification signature.

Algorithm (per Midtrans BI-SNAP docs):
    bodyHash     = lowercase-hex SHA-256 of the raw notification body bytes
    stringToSign = f"POST:{path}:{bodyHash}:{timestamp}"
    verify       = RSA-SHA256(midtransPublicKey, stringToSign, signature)

Note: notification verification uses the Midtrans public key, not the
merchant's private key. This is the third independent BI-SNAP signature
helper — never reuse the access-token or transactional signing code here.

Inputs:
    --path           notification endpoint path on YOUR server, e.g.
                     /api/bisnap/notify (matches what Midtrans signed)
    --timestamp      X-TIMESTAMP header value
    --signature      X-SIGNATURE header value (base64)
    --body-file PATH the raw notification body (use - for stdin)
    --public-key-file PATH | env BISNAP_PUBLIC_KEY

Exit codes: 0 = signature valid, 1 = invalid, 2 = bad input.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import os
import sys

try:
    from sign_bisnap_access_token import normalize_pem
except Exception:
    # When invoked from a different cwd, fall back to inline normalization.
    import re

    def normalize_pem(value: str) -> str:  # type: ignore[no-redef]
        text = value.strip().replace("\\n", "\n")
        if "BEGIN" in text:
            match = re.search(
                r"-----BEGIN ([^-]+)-----\s*([A-Za-z0-9+/=\s]+?)\s*-----END \1-----",
                text,
            )
            if match:
                label, body = match.group(1), match.group(2)
                wrapped = "\n".join(
                    re.findall(".{1,64}", body.replace("\n", "").replace(" ", ""))
                )
                return f"-----BEGIN {label}-----\n{wrapped}\n-----END {label}-----"
            return text
        try:
            return base64.b64decode(text).decode("utf-8").replace("\\n", "\n")
        except Exception as exc:
            raise ValueError(f"public key is not PEM and not base64 PEM: {exc}") from exc


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--path", required=True)
    parser.add_argument("--timestamp", required=True)
    parser.add_argument("--signature", required=True)
    parser.add_argument("--body-file", required=True)
    parser.add_argument("--public-key-file", default=None)
    args = parser.parse_args(argv)

    if args.public_key_file:
        try:
            with open(args.public_key_file, "r", encoding="utf-8") as fh:
                raw_key = fh.read()
        except FileNotFoundError:
            print(f"public key file not found: {args.public_key_file}", file=sys.stderr)
            return 2
        except OSError as exc:
            print(f"cannot read public key file {args.public_key_file}: {exc}", file=sys.stderr)
            return 2
    elif os.environ.get("BISNAP_PUBLIC_KEY"):
        raw_key = os.environ["BISNAP_PUBLIC_KEY"]
    elif os.environ.get("BISNAP_MIDTRANS_PUBLIC_KEY"):
        raw_key = os.environ["BISNAP_MIDTRANS_PUBLIC_KEY"]
    else:
        print("--public-key-file or BISNAP_PUBLIC_KEY env required", file=sys.stderr)
        return 2

    try:
        pem = normalize_pem(raw_key)
    except ValueError as exc:
        print(f"invalid public key input: {exc}", file=sys.stderr)
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
    string_to_sign = f"POST:{args.path}:{body_hash}:{args.timestamp}".encode("utf-8")

    try:
        from cryptography.hazmat.primitives import hashes, serialization
        from cryptography.hazmat.primitives.asymmetric import padding
        from cryptography.exceptions import InvalidSignature
    except ImportError:
        print("cryptography package missing. Install with: pip install cryptography", file=sys.stderr)
        return 2

    try:
        public_key = serialization.load_pem_public_key(pem.encode("utf-8"))
    except (TypeError, ValueError) as exc:
        print(f"invalid public key PEM: {exc}", file=sys.stderr)
        return 2

    try:
        signature_bytes = base64.b64decode(args.signature)
    except Exception as exc:
        print(f"invalid base64 signature: {exc}", file=sys.stderr)
        return 2

    try:
        public_key.verify(  # type: ignore[union-attr]
            signature_bytes, string_to_sign, padding.PKCS1v15(), hashes.SHA256()
        )
    except InvalidSignature:
        print("INVALID", file=sys.stderr)
        print(f"  path       = {args.path}", file=sys.stderr)
        print(f"  timestamp  = {args.timestamp}", file=sys.stderr)
        print(f"  body_hash  = {body_hash}", file=sys.stderr)
        return 1

    print("OK")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
