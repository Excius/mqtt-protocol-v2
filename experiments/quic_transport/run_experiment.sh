#!/usr/bin/env bash

set -euo pipefail
export PYTHON_BIN="$HOME/.venv/bin/python"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EXP_NAME="quic_transport"
RESULTS_DIR="$ROOT_DIR/results/$EXP_NAME"

echo "=== Generating TLS Certificates ==="
$ROOT_DIR/experiments/baseline/generate_tls_certs.sh
export MQTT_TLS_CERT_FILE="$ROOT_DIR/experiments/baseline/certs/server.cert.pem"
export MQTT_TLS_KEY_FILE="$ROOT_DIR/experiments/baseline/certs/server.key.pem"
export MQTT_TLS_CA_FILE="$ROOT_DIR/experiments/baseline/certs/ca.cert.pem"
export MQTT_TLS_INSECURE_SKIP_VERIFY="true"

echo "=== Running Baseline (TCP + TLS) ==="
export MQTT_BROKER_MODULES=""
export MQTT_BROKER_URL="tcps://127.0.0.1:1883"
$ROOT_DIR/experiments/baseline/run_present_state_capture.sh "$RESULTS_DIR/baseline"

echo "=== Running QUIC Transport (Variable) ==="
export MQTT_BROKER_MODULES="quic-transport"
export MQTT_BROKER_URL="quic://127.0.0.1:1883"
$ROOT_DIR/experiments/baseline/run_present_state_capture.sh "$RESULTS_DIR/module"

echo "=== Generating Comparison Plots ==="
"$HOME/.venv/bin/python" $ROOT_DIR/experiments/plot_comparison.py \
    --baseline-dir "$RESULTS_DIR/baseline" \
    --module-dir "$RESULTS_DIR/module" \
    --module-name "QUIC Transport" \
    --output-dir "$RESULTS_DIR/comparison_plots"

echo "Done! Comparison plots saved to $RESULTS_DIR/comparison_plots"
