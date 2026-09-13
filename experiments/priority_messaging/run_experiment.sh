#!/usr/bin/env bash

set -euo pipefail
export PYTHON_BIN="$HOME/.venv/bin/python"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EXP_NAME="priority_messaging"
RESULTS_DIR="$ROOT_DIR/results/$EXP_NAME"

echo "=== Running Baseline (Control) ==="
export MQTT_BROKER_MODULES=""
export MQTT_PRIORITY=""
$ROOT_DIR/experiments/baseline/run_present_state_capture.sh "$RESULTS_DIR/baseline"

echo "=== Running Priority Messaging (Variable) ==="
export MQTT_BROKER_MODULES="priority-messaging"
export MQTT_PRIORITY="Urgent"
$ROOT_DIR/experiments/baseline/run_present_state_capture.sh "$RESULTS_DIR/module"

echo "=== Generating Comparison Plots ==="
"$HOME/.venv/bin/python" $ROOT_DIR/experiments/plot_comparison.py \
    --baseline-dir "$RESULTS_DIR/baseline" \
    --module-dir "$RESULTS_DIR/module" \
    --module-name "Priority Messaging" \
    --output-dir "$RESULTS_DIR/comparison_plots"

echo "Done! Comparison plots saved to $RESULTS_DIR/comparison_plots"
