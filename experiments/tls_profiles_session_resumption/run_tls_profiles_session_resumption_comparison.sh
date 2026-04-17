#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BASE_SCRIPT="$ROOT_DIR/experiments/tls_profiles/run_tls_profiles_comparison.sh"

if [[ ! -f "$BASE_SCRIPT" ]]; then
  echo "Base script not found: $BASE_SCRIPT" >&2
  exit 1
fi

if [[ "$#" -gt 1 ]]; then
  echo "Usage: bash $(basename "$0") [results_dir]" >&2
  exit 1
fi

RESULTS_ARG="${1:-results/tls_profiles_session_resumption}"
if [[ "$RESULTS_ARG" = /* ]]; then
  COMBO_RESULTS_DIR="$RESULTS_ARG"
else
  COMBO_RESULTS_DIR="$ROOT_DIR/$RESULTS_ARG"
fi

echo "Running experiment with combined modules: tls-session-resumption + adaptive-tls-profiles"
echo "TLS profiles: ${TLS_PROFILES:-LOW_POWER,BALANCED,HIGH_SECURITY}"
echo "Results directory: $COMBO_RESULTS_DIR"

RESULTS_ROOT="$COMBO_RESULTS_DIR" \
TLS_PROFILE_MODULES="adaptive-tls-profiles" \
TLS_PROFILE_EXTRA_MODULES="tls-session-resumption" \
CONNECT_TLS_SESSION_CACHE_SIZE="${CONNECT_TLS_SESSION_CACHE_SIZE:-200}" \
LOAD_TLS_SESSION_CACHE_SIZE="${LOAD_TLS_SESSION_CACHE_SIZE:-200}" \
bash "$BASE_SCRIPT" "$COMBO_RESULTS_DIR"
