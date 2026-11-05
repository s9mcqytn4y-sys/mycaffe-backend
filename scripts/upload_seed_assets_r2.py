#!/usr/bin/env python3
"""
Uploads all generated .webp seed assets in docs/assets/seeds/ to Cloudflare R2 bucket 'mycaffe-media'
under the 'seeds/' prefix (served publicly at https://cdn.abbdullaziz.online/seeds/...).
Uses `wrangler r2 bulk put` with 20 concurrent workers for fast, reliable upload.
"""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ASSETS_DIR = Path(__file__).resolve().parent.parent / "docs" / "assets" / "seeds"
BUCKET_NAME = "mycaffe-media"


def main():
    webp_files = sorted(ASSETS_DIR.rglob("*.webp"))
    if not webp_files:
        print("No .webp files found in docs/assets/seeds/", file=sys.stderr)
        sys.exit(1)

    entries = []
    total_bytes = 0
    for p in webp_files:
        rel_key = "seeds/" + p.relative_to(ASSETS_DIR).as_posix()
        entries.append({"key": rel_key, "file": str(p.resolve())})
        total_bytes += p.stat().st_size

    wrangler_bin = Path(os.environ.get("APPDATA", "")) / "npm" / "wrangler.cmd"
    wrangler_cmd = str(wrangler_bin) if wrangler_bin.exists() else "wrangler.cmd"

    # Ensure Wrangler uses its full OAuth session rather than a restricted DNS-only CLOUDFLARE_API_TOKEN in .env
    env = os.environ.copy()
    env.pop("CLOUDFLARE_API_TOKEN", None)

    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as tmp:
        json.dump(entries, tmp, indent=2)
        bulk_json_path = tmp.name

    try:
        print(f"Uploading {len(entries)} WebP files ({total_bytes / 1024:.1f} KB) to R2 bucket '{BUCKET_NAME}' via wrangler r2 bulk put...")
        cmd = [
            wrangler_cmd,
            "r2",
            "bulk",
            "put",
            BUCKET_NAME,
            "--filename",
            bulk_json_path,
            "--content-type",
            "image/webp",
            "--cache-control",
            "public, max-age=31536000, immutable",
            "--concurrency",
            "20",
            "--remote",
        ]
        # Run from temp dir so Wrangler does not auto-load .env with restricted CLOUDFLARE_API_TOKEN
        res = subprocess.run(cmd, cwd=tempfile.gettempdir(), env=env, check=False)
        if res.returncode != 0:
            sys.exit(res.returncode)
        print(f"Successfully uploaded all {len(entries)} WebP files to https://cdn.abbdullaziz.online/seeds/!")
    finally:
        Path(bulk_json_path).unlink(missing_ok=True)


if __name__ == "__main__":
    main()
