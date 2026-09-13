#!/usr/bin/env bash

set -euo pipefail
export PYTHON_BIN="$HOME/.venv/bin/python"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EXP_NAME="wildcard_tokens"
RESULTS_DIR="$ROOT_DIR/results/$EXP_NAME"

echo "=== Running Baseline (Control) ==="
export MQTT_BROKER_MODULES=""
$ROOT_DIR/experiments/baseline/run_present_state_capture.sh "$RESULTS_DIR/baseline"

echo "=== Running Wildcard Capability Tokens (Variable) ==="
export MQTT_BROKER_MODULES="wildcard-tokens"
export MQTT_CLIENT_ID="admin"
export MQTT_TOPIC="#"

# The broker isn't up yet at this point, and the subscriber has no
# reconnect loop of its own (it dials once and exits on failure), so a
# single background launch here would race the broker's startup and most
# likely never actually connect. Keep retrying until run_present_state_capture.sh
# finishes so the "#" subscription (and the wildcard-token ACL check it
# exercises) is actually live for the module capture window.
SUB_STOP_FILE="$(mktemp)"
rm -f "$SUB_STOP_FILE"
(
  cd "$ROOT_DIR"
  while [[ ! -f "$SUB_STOP_FILE" ]]; do
    # set -e is inherited into this subshell; without `|| true` the very
    # first attempt (which normally fails because the broker isn't up yet)
    # would trigger errexit and kill this whole retry loop on the spot,
    # silently, since stderr is discarded below.
    go run ./client/subscriber >/dev/null 2>&1 || true
    sleep 1
  done
) &
SUB_LOOP_PID=$!

$ROOT_DIR/experiments/baseline/run_present_state_capture.sh "$RESULTS_DIR/module" || true

touch "$SUB_STOP_FILE"
pkill -P "$SUB_LOOP_PID" 2>/dev/null || true
kill "$SUB_LOOP_PID" 2>/dev/null || true
wait "$SUB_LOOP_PID" 2>/dev/null || true
rm -f "$SUB_STOP_FILE"

echo "=== Generating Comparison Plots ==="
"$HOME/.venv/bin/python" $ROOT_DIR/experiments/plot_comparison.py \
    --baseline-dir "$RESULTS_DIR/baseline" \
    --module-dir "$RESULTS_DIR/module" \
    --module-name "Wildcard Tokens" \
    --output-dir "$RESULTS_DIR/comparison_plots"

echo "Done! Comparison plots saved to $RESULTS_DIR/comparison_plots"
