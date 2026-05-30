#!/bin/bash
# Wrapper for scripts/docs/update_continuation.py
# Updates docs/continuation.md §1 and §2 in-place per Constitution §12.10

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 "${SCRIPT_DIR}/update_continuation.py"
