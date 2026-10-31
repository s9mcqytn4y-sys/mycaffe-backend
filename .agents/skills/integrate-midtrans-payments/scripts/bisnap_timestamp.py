#!/usr/bin/env python3
"""Print a BI-SNAP-compatible Asia/Jakarta ISO-8601 timestamp.

Output format: YYYY-MM-DDTHH:MM:SS+07:00

Used for the X-TIMESTAMP header on all BI-SNAP requests. Avoid relying on
the local machine timezone; this script always emits Asia/Jakarta time
regardless of the host clock setting.

Inputs:
    --offset-seconds N    advance or rewind the clock by N seconds (for
                          replay testing and signature-timestamp drift
                          experiments). Defaults to 0.

Exit codes: 0 always (unless argparse fails).
"""

from __future__ import annotations

import argparse
import sys
from datetime import datetime, timedelta, timezone

JAKARTA = timezone(timedelta(hours=7))


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--offset-seconds",
        type=int,
        default=0,
        help="Shift the emitted timestamp by N seconds; default 0",
    )
    args = parser.parse_args(argv)
    moment = datetime.now(JAKARTA) + timedelta(seconds=args.offset_seconds)
    print(moment.strftime("%Y-%m-%dT%H:%M:%S+07:00"))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
