#!/usr/bin/env bash
# Thin wrapper: detect AI agents on this machine and install Stacker skills.
# Prefer the Python installer (scripts/install_skills.py).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec python3 "${ROOT}/scripts/install_skills.py" "$@"
