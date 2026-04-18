#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RESULTS_ROOT="${RESULTS_ROOT:-$ROOT_DIR/results/property_validator}"
RESULTS_ARG="${1:-}"
if [[ -z "$RESULTS_ARG" ]]; then
  RESULTS_DIR="$RESULTS_ROOT"
elif [[ "$RESULTS_ARG" = /* ]]; then
  RESULTS_DIR="$RESULTS_ARG"
else
  RESULTS_DIR="$ROOT_DIR/$RESULTS_ARG"
fi
RAW_DIR="$RESULTS_DIR/raw"
PLOTS_DIR="$RESULTS_DIR/plots"
METADATA_DIR="$RESULTS_DIR/metadata"
BIN_DIR="$RESULTS_DIR/bin"
SUMMARY_CSV="$RESULTS_DIR/summary.csv"
RUN_INFO_TXT="$RESULTS_DIR/run_info.txt"
MANIFEST_CSV="$RESULTS_DIR/dataset_manifest.csv"
PLOT_LOG="$RESULTS_DIR/plot_generation.log"

PYTHON_BIN="${PYTHON_BIN:-python3}"
BROKER_DIR="$ROOT_DIR/broker"
PLOT_SCRIPT="$ROOT_DIR/experiments/property_validator/plot_property_validator.py"
INJECTOR_CLIENT_BIN="$BIN_DIR/property_injector"
BROKER_BIN="$BIN_DIR/broker"

MQTT_BROKER_HOST="${MQTT_BROKER_HOST:-127.0.0.1}"
MQTT_BROKER_PORT_BASE="${MQTT_BROKER_PORT_BASE:-48883}"
MQTT_WS_PORT_BASE="${MQTT_WS_PORT_BASE:-48882}"
MQTT_INFO_PORT_BASE="${MQTT_INFO_PORT_BASE:-48080}"
MQTT_BROKER_PORT=""
MQTT_WS_PORT=""
MQTT_INFO_PORT=""
MQTT_BROKER_ADDR=""
MQTT_WS_ADDR=""
MQTT_INFO_ADDR=""
MQTT_BROKER_URL=""
MQTT_INFO_URL=""

ATTACK_CONCURRENCY="${ATTACK_CONCURRENCY:-20}"
ATTACK_DURATION_S="${ATTACK_DURATION_S:-30}"
ATTACK_PROP_COUNT="${ATTACK_PROP_COUNT:-50}"
ATTACK_KEY_SIZE="${ATTACK_KEY_SIZE:-200}"
ATTACK_VAL_SIZE="${ATTACK_VAL_SIZE:-200}"
STATS_SAMPLE_INTERVAL_MS="${STATS_SAMPLE_INTERVAL_MS:-200}"

BROKER_PID=""
LOAD_PID=""
BROKER_MONITOR_PID=""

mkdir -p "$RESULTS_DIR"
rm -rf "$RAW_DIR" "$PLOTS_DIR" "$METADATA_DIR" "$BIN_DIR"
rm -f "$SUMMARY_CSV" "$RUN_INFO_TXT" "$MANIFEST_CSV" "$PLOT_LOG"
mkdir -p "$RAW_DIR" "$PLOTS_DIR" "$METADATA_DIR" "$BIN_DIR"

cat >"$SUMMARY_CSV" <<'EOF'
scenario,modules,broker_cpu_peak_pct,broker_mem_peak_mib,throughput_msgs_per_s,total_sent,total_errors,broker_log,broker_stats_csv,load_log
EOF

cleanup() {
  if [[ -n "$BROKER_MONITOR_PID" ]] && kill -0 "$BROKER_MONITOR_PID" 2>/dev/null; then
    kill "$BROKER_MONITOR_PID" 2>/dev/null || true
    wait "$BROKER_MONITOR_PID" 2>/dev/null || true
  fi
  if [[ -n "$LOAD_PID" ]] && kill -0 "$LOAD_PID" 2>/dev/null; then
    kill "$LOAD_PID" 2>/dev/null || true
    wait "$LOAD_PID" 2>/dev/null || true
  fi
  if [[ -n "$BROKER_PID" ]] && kill -0 "$BROKER_PID" 2>/dev/null; then
    kill "$BROKER_PID" 2>/dev/null || true
    wait "$BROKER_PID" 2>/dev/null || true
  fi
}

trap cleanup EXIT

port_is_busy() {
  local port="$1"
  if ! command -v ss >/dev/null 2>&1; then
    return 1
  fi
  ss -ltn "sport = :$port" 2>/dev/null | awk 'NR > 1 {found=1} END {exit found ? 0 : 1}'
}

find_free_port() {
  local start_port="$1"
  local end_port=$((start_port + 300))
  local port
  for ((port = start_port; port <= end_port; port++)); do
    if ! port_is_busy "$port"; then
      echo "$port"
      return 0
    fi
  done
  return 1
}

configure_ports() {
  local scenario_index="$1"
  local broker_start=$((MQTT_BROKER_PORT_BASE + (scenario_index * 10)))
  local ws_start=$((MQTT_WS_PORT_BASE + (scenario_index * 10)))
  local info_start=$((MQTT_INFO_PORT_BASE + (scenario_index * 10)))

  MQTT_BROKER_PORT="$(find_free_port "$broker_start")"
  MQTT_WS_PORT="$(find_free_port "$ws_start")"
  MQTT_INFO_PORT="$(find_free_port "$info_start")"

  MQTT_BROKER_ADDR=":${MQTT_BROKER_PORT}"
  MQTT_WS_ADDR=":${MQTT_WS_PORT}"
  MQTT_INFO_ADDR=":${MQTT_INFO_PORT}"
  MQTT_BROKER_URL="${MQTT_BROKER_HOST}:${MQTT_BROKER_PORT}"
  MQTT_INFO_URL="http://127.0.0.1:${MQTT_INFO_PORT}/"
}

start_broker() {
  local modules="$1"
  local broker_log="$2"

  "$BROKER_BIN" \
    --tcp "$MQTT_BROKER_ADDR" \
    --ws "$MQTT_WS_ADDR" \
    --info "$MQTT_INFO_ADDR" \
    --modules "$modules" \
    >"$broker_log" 2>&1 &
  BROKER_PID="$!"

  for _ in $(seq 1 120); do
    if ! kill -0 "$BROKER_PID" 2>/dev/null; then
      echo "Broker process exited before ready state." >&2
      return 1
    fi
    if curl -sf --max-time 0.3 "$MQTT_INFO_URL" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done

  echo "Broker did not become ready on $MQTT_INFO_URL" >&2
  return 1
}

stop_broker() {
  if [[ -n "$BROKER_PID" ]] && kill -0 "$BROKER_PID" 2>/dev/null; then
    kill "$BROKER_PID" 2>/dev/null || true
    wait "$BROKER_PID" 2>/dev/null || true
  fi
  BROKER_PID=""
}

monitor_process_stats() {
  local target_pid="$1"
  local out_csv="$2"
  local sleep_seconds
  sleep_seconds="$(awk -v ms="$STATS_SAMPLE_INTERVAL_MS" 'BEGIN {printf "%.3f", ms/1000}')"
  echo "timestamp_iso,broker_cpu,broker_rss" >"$out_csv"

  while kill -0 "$LOAD_PID" 2>/dev/null; do
    read -r cpu rss _ < <(ps -p "$target_pid" -o %cpu=,rss=,vsz= | awk 'NR==1 {print $1, $2, $3}')
    if [[ -n "${cpu:-}" ]]; then
      printf "%s,%s,%s\n" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$cpu" "${rss:-0}" >>"$out_csv"
    fi
    sleep "$sleep_seconds"
  done
}

run_scenario() {
  local scenario="$1"
  local modules="$2"
  local scenario_index="$3"

  local scenario_dir="$RAW_DIR/$scenario"
  local broker_log="$scenario_dir/broker.log"
  local broker_stats_csv="$scenario_dir/broker_stats.csv"
  local load_log="$scenario_dir/load.log"

  mkdir -p "$scenario_dir"
  echo "Running scenario=$scenario modules=$modules"

  configure_ports "$scenario_index"
  start_broker "$modules" "$broker_log"

  "$INJECTOR_CLIENT_BIN" \
    -broker "$MQTT_BROKER_URL" \
    -concurrency "$ATTACK_CONCURRENCY" \
    -duration "$ATTACK_DURATION_S" \
    -prop-count "$ATTACK_PROP_COUNT" \
    -key-size "$ATTACK_KEY_SIZE" \
    -val-size "$ATTACK_VAL_SIZE" \
    >"$load_log" 2>&1 &
  LOAD_PID="$!"

  monitor_process_stats "$BROKER_PID" "$broker_stats_csv" &
  BROKER_MONITOR_PID="$!"

  wait "$LOAD_PID"
  LOAD_PID=""

  if [[ -n "$BROKER_MONITOR_PID" ]]; then
    kill "$BROKER_MONITOR_PID" 2>/dev/null || true
    wait "$BROKER_MONITOR_PID" 2>/dev/null || true
    BROKER_MONITOR_PID=""
  fi

  stop_broker

  # Process stats
  local broker_cpu_peak broker_mem_peak
  broker_cpu_peak="$(awk -F, 'NR>1 && $2>max{max=$2} END{printf "%.3f", max}' "$broker_stats_csv")"
  broker_mem_peak="$(awk -F, 'NR>1 {rss=$3/1024} rss>max{max=rss} END{printf "%.3f", max}' "$broker_stats_csv")"

  local total_sent total_errors load_duration throughput
  local summary_line
  summary_line="$(grep "^SUMMARY " "$load_log" | tail -n 1 || true)"
  
  if [[ -n "$summary_line" ]]; then
    load_duration="$(awk '{for(i=1;i<=NF;i++) if($i~/^duration_seconds=/) {split($i,a,"="); print a[2]}}' <<<"$summary_line")"
    total_sent="$(awk '{for(i=1;i<=NF;i++) if($i~/^total_sent=/) {split($i,a,"="); print a[2]}}' <<<"$summary_line")"
    total_errors="$(awk '{for(i=1;i<=NF;i++) if($i~/^total_errors=/) {split($i,a,"="); print a[2]}}' <<<"$summary_line")"
    if awk "BEGIN {exit !($load_duration > 0)}"; then
      throughput="$(awk -v msgs="$total_sent" -v sec="$load_duration" 'BEGIN{printf "%.3f", msgs/sec}')"
    else
      throughput="0"
    fi
  else
    load_duration="0"
    total_sent="0"
    total_errors="0"
    throughput="0"
  fi

  printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" \
    "$scenario" \
    "$modules" \
    "$broker_cpu_peak" \
    "$broker_mem_peak" \
    "$throughput" \
    "$total_sent" \
    "$total_errors" \
    "$broker_log" \
    "$broker_stats_csv" \
    "$load_log" >>"$SUMMARY_CSV"
}

ensure_plot_dependencies() {
  if ! "$PYTHON_BIN" -c 'import matplotlib, pandas' >/dev/null 2>&1; then
    echo "matplotlib and pandas are required for plot generation. Install them with: $PYTHON_BIN -m pip install matplotlib pandas" >&2
    return 1
  fi
}

echo "Building binaries..."
(cd "$BROKER_DIR" && go build -o "$BROKER_BIN" ./cmd/main.go)
(cd "$ROOT_DIR" && go build -o "$INJECTOR_CLIENT_BIN" ./client/property_injector/main.go)

ensure_plot_dependencies

run_scenario "baseline_no_defense" "baseline" "0"
run_scenario "with_defense" "property-validator" "1"

if [[ ! -f "$PLOT_SCRIPT" ]]; then
  echo "Plot script not found: $PLOT_SCRIPT" >&2
  exit 1
fi

"$PYTHON_BIN" "$PLOT_SCRIPT" \
  --summary-csv "$SUMMARY_CSV" \
  --raw-dir "$RAW_DIR" \
  --output-dir "$PLOTS_DIR" >"$PLOT_LOG" 2>&1

cat >"$MANIFEST_CSV" <<EOF
path,type
summary.csv,summary
plots/cpu_memory_comparison.png,plot
EOF

echo "Property validator experiment complete."
echo "Results directory: $RESULTS_DIR"
