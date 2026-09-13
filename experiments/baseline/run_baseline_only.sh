#!/usr/bin/env bash
#
# Captures a single, authoritative "vanilla MQTTv5" performance snapshot with
# every optional module disabled and TLS session resumption explicitly
# turned off. This is the reference point every per-module and combined
# comparison in this repo should be measured against.
#
# Usage:
#   ./experiments/baseline/run_baseline_only.sh [results_dir]
#
# results_dir defaults to results/baseline_mqttv5. Pass an absolute path, or
# a path relative to the repo root.

set -euo pipefail
export PYTHON_BIN="${PYTHON_BIN:-$HOME/.venv/bin/python}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RESULTS_DIR="${1:-$ROOT_DIR/results/baseline_mqttv5}"

# resolve_broker_modules() in run_present_state_capture.sh falls back to
# enabling tls-session-resumption whenever MQTT_BROKER_MODULES is empty and
# MQTT_TLS_SESSION_RESUMPTION isn't explicitly "false". Set both explicitly
# so this really is the zero-module baseline, not an accidental TLS-ticket
# variant of it.
export MQTT_BROKER_MODULES="baseline"
export MQTT_TLS_SESSION_RESUMPTION="false"

# No TLS certs, no integrity secret, no priority tagging: plain TCP MQTTv5.
export MQTT_TLS_CERT_FILE=""
export MQTT_TLS_KEY_FILE=""
export MQTT_TLS_CA_FILE=""
export MQTT_INTEGRITY_SECRET=""
export MQTT_PRIORITY=""

echo "=== Capturing pure MQTTv5 baseline (no modules) ==="
"$ROOT_DIR/experiments/baseline/run_present_state_capture.sh" "$RESULTS_DIR"

echo "Done! Baseline MQTTv5 results saved to $RESULTS_DIR"
echo "Use this directory as --baseline-dir when comparing any other module or combined run with experiments/plot_comparison.py."
