#!/usr/bin/env bash
#
# Demonstrates the actual "urgent jumps the queue" behavior of the
# priority-messaging module against a real broker under contention — the
# other priority_messaging experiment (run_experiment.sh) only ever sends one
# uniform priority value, so it measures overhead but never shows reordering.
#
# Methodology: each trial fires a large flood of Normal-priority messages
# from many concurrent connections (concurrency is what builds a genuine
# backlog — a single connection can't outpace the broker's own scheduler),
# with one Urgent-priority message interleaved partway through one of those
# same connections' own send sequence (deliberately not a separate dedicated
# connection, which would give it an unrelated scheduling advantage). We then
# record the Urgent message's receive RANK out of the whole flood: rank 1
# means it was delivered first despite being sent from the middle of the
# pack. Run once with no modules (baseline) and once with priority-messaging,
# and compare.
#
# Usage:
#   ./experiments/priority_messaging/run_ordering_experiment.sh

set -euo pipefail
export PYTHON_BIN="${PYTHON_BIN:-$HOME/.venv/bin/python}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RESULTS_DIR="$ROOT_DIR/results/priority_messaging_ordering"
mkdir -p "$RESULTS_DIR"

TRIALS="${TRIALS:-20}"
FLOOD="${FLOOD:-3000}"
FLOOD_CONNS="${FLOOD_CONNS:-30}"
WAIT_MS="${WAIT_MS:-2000}"

BROKER_ADDR=":1883"
BROKER_URL="tcp://127.0.0.1:1883"
BROKER_PID=""

cleanup() {
  if [[ -n "$BROKER_PID" ]] && kill -0 "$BROKER_PID" 2>/dev/null; then
    kill "$BROKER_PID" 2>/dev/null || true
    wait "$BROKER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

wait_for_broker() {
  for _ in $(seq 1 40); do
    if curl -sf --max-time 0.2 "http://127.0.0.1:8080/" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  echo "Broker did not become ready" >&2
  return 1
}

run_scenario() {
  local label="$1"
  local modules="$2"
  local out_csv="$RESULTS_DIR/${label}.csv"
  local broker_log="$RESULTS_DIR/${label}_broker.log"

  echo "=== Running scenario: $label (modules=$modules) ==="
  (
    cd "$ROOT_DIR/broker"
    exec go run ./cmd/main.go --tcp "$BROKER_ADDR" --info :8080 --modules "$modules" >"$broker_log" 2>&1
  ) &
  BROKER_PID=$!
  wait_for_broker

  (
    cd "$ROOT_DIR"
    go run ./client/priority_bench \
      --broker "$BROKER_URL" \
      --trials "$TRIALS" \
      --flood "$FLOOD" \
      --flood-conns "$FLOOD_CONNS" \
      --wait-ms "$WAIT_MS" \
      --label "$label" \
      --out "$out_csv"
  )

  kill "$BROKER_PID" 2>/dev/null || true
  wait "$BROKER_PID" 2>/dev/null || true
  BROKER_PID=""
}

run_scenario "baseline" "baseline"
run_scenario "module" "priority-messaging"

echo "Done. Raw CSVs: $RESULTS_DIR/baseline.csv, $RESULTS_DIR/module.csv"
