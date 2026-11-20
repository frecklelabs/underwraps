#!/usr/bin/env bash
set -euo pipefail
# Keep findings and sensitive diagnostics out of public logs and artifacts.
if ! python3 scripts/leakcheck/scan.py > /dev/null 2>&1; then
  echo 'Secret scan failed; inspect locally with the pinned scanner.'
  exit 1
fi
