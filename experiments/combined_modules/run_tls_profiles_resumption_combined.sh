#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BASE_SCRIPT="$ROOT_DIR/experiments/tls_profiles/run_tls_profiles_comparison.sh"
PLOT_SCRIPT="$ROOT_DIR/experiments/combined_modules/plot_tls_resumption_combined.py"
PYTHON_BIN="${PYTHON_BIN:-python3}"

if [[ ! -f "$BASE_SCRIPT" ]]; then
  echo "Base script not found: $BASE_SCRIPT" >&2
  exit 1
fi

RESULTS_ARG="${1:-results/tls_profiles_resumption_combined}"
if [[ "$RESULTS_ARG" = /* ]]; then
  COMBINED_RESULTS_DIR="$RESULTS_ARG"
else
  COMBINED_RESULTS_DIR="$ROOT_DIR/$RESULTS_ARG"
fi

echo "Running combined TLS modules experiment: adaptive-tls-profiles + tls-session-resumption"
echo "TLS profiles: ${TLS_PROFILES:-LOW_POWER,BALANCED,HIGH_SECURITY}"
echo "Results directory: $COMBINED_RESULTS_DIR"

RESULTS_ROOT="$COMBINED_RESULTS_DIR" \
TLS_PROFILE_MODULES="adaptive-tls-profiles" \
TLS_PROFILE_EXTRA_MODULES="tls-session-resumption" \
CONNECT_TLS_SESSION_CACHE_SIZE="${CONNECT_TLS_SESSION_CACHE_SIZE:-200}" \
LOAD_TLS_SESSION_CACHE_SIZE="${LOAD_TLS_SESSION_CACHE_SIZE:-200}" \
bash "$BASE_SCRIPT" "$COMBINED_RESULTS_DIR"

if [[ ! -f "$PLOT_SCRIPT" ]]; then
  echo "Plot script not found: $PLOT_SCRIPT" >&2
  exit 1
fi

SUMMARY_CSV="$COMBINED_RESULTS_DIR/summary.csv"
PLOTS_DIR="$COMBINED_RESULTS_DIR/plots"
PLOT_LOG="$COMBINED_RESULTS_DIR/combined_plot_generation.log"

"$PYTHON_BIN" "$PLOT_SCRIPT" \
  --summary-csv "$SUMMARY_CSV" \
  --output-dir "$PLOTS_DIR" >"$PLOT_LOG" 2>&1

for required_file in \
  "$PLOTS_DIR/tls_resumption_combined_overview.png" \
  "$PLOTS_DIR/tls_resumption_combined_manifest.csv"; do
  if [[ ! -f "$required_file" ]]; then
    echo "Missing expected plot artifact: $required_file" >&2
    echo "Plot log: $PLOT_LOG" >&2
    exit 1
  fi
done
