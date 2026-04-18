#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RESULTS_ROOT="${RESULTS_ROOT:-$ROOT_DIR/results/auth_property_combined}"
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
BIN_DIR="$RESULTS_DIR/bin"
METADATA_DIR="$RESULTS_DIR/metadata"
SUMMARY_CSV="$RESULTS_DIR/summary.csv"
RUN_INFO_TXT="$RESULTS_DIR/run_info.txt"
MANIFEST_CSV="$RESULTS_DIR/dataset_manifest.csv"
PLOT_LOG="$RESULTS_DIR/plot_generation.log"

PYTHON_BIN="${PYTHON_BIN:-python3}"
PLOT_SCRIPT="$ROOT_DIR/experiments/combined_modules/plot_auth_property_defense.py"

BROKER_DIR="$ROOT_DIR/broker"
BROKER_BIN="$BIN_DIR/broker"
AUTH_INJECTOR_BIN="$BIN_DIR/auth_injector"
PROPERTY_INJECTOR_BIN="$BIN_DIR/property_injector"

MQTT_BROKER_HOST="${MQTT_BROKER_HOST:-127.0.0.1}"
MQTT_BROKER_PORT_BASE="${MQTT_BROKER_PORT_BASE:-58883}"
MQTT_WS_PORT_BASE="${MQTT_WS_PORT_BASE:-58882}"
MQTT_INFO_PORT_BASE="${MQTT_INFO_PORT_BASE:-58080}"
MQTT_BROKER_PORT=""
MQTT_WS_PORT=""
MQTT_INFO_PORT=""
MQTT_BROKER_ADDR=""
MQTT_WS_ADDR=""
MQTT_INFO_ADDR=""
MQTT_BROKER_TARGET=""
MQTT_INFO_URL=""

AUTH_ATTACK_CONCURRENCY="${AUTH_ATTACK_CONCURRENCY:-50}"
AUTH_ATTACK_DURATION_S="${AUTH_ATTACK_DURATION_S:-30}"
AUTH_ATTACK_TYPE="${AUTH_ATTACK_TYPE:-flood}"

PROPERTY_ATTACK_CONCURRENCY="${PROPERTY_ATTACK_CONCURRENCY:-20}"
PROPERTY_ATTACK_DURATION_S="${PROPERTY_ATTACK_DURATION_S:-30}"
PROPERTY_ATTACK_PROP_COUNT="${PROPERTY_ATTACK_PROP_COUNT:-50}"
PROPERTY_ATTACK_KEY_SIZE="${PROPERTY_ATTACK_KEY_SIZE:-200}"
PROPERTY_ATTACK_VAL_SIZE="${PROPERTY_ATTACK_VAL_SIZE:-200}"

STATS_SAMPLE_INTERVAL_MS="${STATS_SAMPLE_INTERVAL_MS:-200}"

BROKER_PID=""
ATTACK_PID=""
MONITOR_PID=""

mkdir -p "$RESULTS_DIR"
rm -rf "$RAW_DIR" "$PLOTS_DIR" "$BIN_DIR" "$METADATA_DIR"
rm -f "$SUMMARY_CSV" "$RUN_INFO_TXT" "$MANIFEST_CSV" "$PLOT_LOG"
mkdir -p "$RAW_DIR" "$PLOTS_DIR" "$BIN_DIR" "$METADATA_DIR"

cat >"$SUMMARY_CSV" <<'EOF'
scenario,attack_type,modules,broker_cpu_peak_pct,broker_mem_peak_mib,attacker_attempt_rate_msgs_per_s,attacker_connections_made,attacker_packets_sent,attacker_send_errors,attack_duration_seconds,broker_log,broker_stats_csv,attack_log
EOF

cleanup() {
  if [[ -n "$MONITOR_PID" ]] && kill -0 "$MONITOR_PID" 2>/dev/null; then
    kill "$MONITOR_PID" 2>/dev/null || true
    wait "$MONITOR_PID" 2>/dev/null || true
  fi
  if [[ -n "$ATTACK_PID" ]] && kill -0 "$ATTACK_PID" 2>/dev/null; then
    kill "$ATTACK_PID" 2>/dev/null || true
    wait "$ATTACK_PID" 2>/dev/null || true
  fi
  if [[ -n "$BROKER_PID" ]] && kill -0 "$BROKER_PID" 2>/dev/null; then
    kill "$BROKER_PID" 2>/dev/null || true
    wait "$BROKER_PID" 2>/dev/null || true
  fi
}

trap cleanup EXIT

extract_summary_value() {
  local line="$1"
  local key="$2"
  awk -v k="$key" '{for (i = 1; i <= NF; i++) {split($i, a, "="); if (a[1] == k) {print a[2]; break}}}' <<<"$line"
}

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
  local broker_start=$((MQTT_BROKER_PORT_BASE + (scenario_index * 20)))
  local ws_start=$((MQTT_WS_PORT_BASE + (scenario_index * 20)))
  local info_start=$((MQTT_INFO_PORT_BASE + (scenario_index * 20)))

  MQTT_BROKER_PORT="$(find_free_port "$broker_start")"
  MQTT_WS_PORT="$(find_free_port "$ws_start")"
  MQTT_INFO_PORT="$(find_free_port "$info_start")"
  if [[ -z "$MQTT_BROKER_PORT" || -z "$MQTT_WS_PORT" || -z "$MQTT_INFO_PORT" ]]; then
    echo "Failed to allocate ports for scenario index $scenario_index" >&2
    return 1
  fi

  MQTT_BROKER_ADDR=":${MQTT_BROKER_PORT}"
  MQTT_WS_ADDR=":${MQTT_WS_PORT}"
  MQTT_INFO_ADDR=":${MQTT_INFO_PORT}"
  MQTT_BROKER_TARGET="${MQTT_BROKER_HOST}:${MQTT_BROKER_PORT}"
  MQTT_INFO_URL="http://127.0.0.1:${MQTT_INFO_PORT}/"
}

start_broker() {
  local modules="$1"
  local broker_log="$2"
  local broker_cmd=(
    "$BROKER_BIN"
    --tcp "$MQTT_BROKER_ADDR"
    --ws "$MQTT_WS_ADDR"
    --info "$MQTT_INFO_ADDR"
    --modules "$modules"
  )

  "${broker_cmd[@]}" >"$broker_log" 2>&1 &
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

monitor_broker_stats() {
  local out_csv="$1"
  local sleep_seconds
  sleep_seconds="$(awk -v ms="$STATS_SAMPLE_INTERVAL_MS" 'BEGIN {printf "%.3f", ms/1000}')"
  echo "timestamp_iso,broker_cpu_pct,broker_rss_kb" >"$out_csv"

  while kill -0 "$ATTACK_PID" 2>/dev/null; do
    read -r cpu rss _ < <(ps -p "$BROKER_PID" -o %cpu=,rss=,vsz= | awk 'NR==1 {print $1, $2, $3}')
    if [[ -n "${cpu:-}" ]]; then
      printf "%s,%s,%s\n" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$cpu" "${rss:-0}" >>"$out_csv"
    fi
    sleep "$sleep_seconds"
  done
}

run_auth_attack() {
  local out_log="$1"
  "$AUTH_INJECTOR_BIN" \
    -broker "$MQTT_BROKER_TARGET" \
    -concurrency "$AUTH_ATTACK_CONCURRENCY" \
    -duration "${AUTH_ATTACK_DURATION_S}s" \
    -type "$AUTH_ATTACK_TYPE" >"$out_log" 2>&1 &
  ATTACK_PID="$!"
}

run_property_attack() {
  local out_log="$1"
  "$PROPERTY_INJECTOR_BIN" \
    -broker "$MQTT_BROKER_TARGET" \
    -concurrency "$PROPERTY_ATTACK_CONCURRENCY" \
    -duration "$PROPERTY_ATTACK_DURATION_S" \
    -prop-count "$PROPERTY_ATTACK_PROP_COUNT" \
    -key-size "$PROPERTY_ATTACK_KEY_SIZE" \
    -val-size "$PROPERTY_ATTACK_VAL_SIZE" >"$out_log" 2>&1 &
  ATTACK_PID="$!"
}

ensure_plot_dependencies() {
  if ! "$PYTHON_BIN" -c 'import matplotlib' >/dev/null 2>&1; then
    echo "matplotlib is required for plot generation. Install it with: $PYTHON_BIN -m pip install matplotlib" >&2
    return 1
  fi
}

append_summary_row() {
  local scenario="$1"
  local attack_type="$2"
  local modules="$3"
  local broker_stats_csv="$4"
  local attack_log="$5"
  local broker_log="$6"

  local broker_cpu_peak broker_mem_peak total_connects total_sent total_errors duration_s throughput
  local modules_csv="${modules//,/+}"
  broker_cpu_peak="$(awk -F, 'NR > 1 && ($2 + 0) > m {m = $2 + 0} END {printf "%.3f", m + 0}' "$broker_stats_csv")"
  broker_mem_peak="$(awk -F, 'NR > 1 && ($3 + 0) > m {m = $3 + 0} END {printf "%.3f", (m + 0) / 1024}' "$broker_stats_csv")"

  total_connects="na"
  total_sent="0"
  total_errors="0"
  duration_s="0"
  throughput="0"

  if [[ "$attack_type" == "auth" ]]; then
    local summary_line
    summary_line="$(grep "Attack complete\\." "$attack_log" | tail -n 1 || true)"
    total_connects="$(sed -n 's/.*Connections Made: \([0-9][0-9]*\).*/\1/p' <<<"$summary_line" | tail -n 1)"
    total_sent="$(sed -n 's/.*Packets Sent: \([0-9][0-9]*\).*/\1/p' <<<"$summary_line" | tail -n 1)"
    total_errors="$(sed -n 's/.*Errors: \([0-9][0-9]*\).*/\1/p' <<<"$summary_line" | tail -n 1)"
    duration_s="$AUTH_ATTACK_DURATION_S"
    total_connects="${total_connects:-0}"
    total_sent="${total_sent:-0}"
    total_errors="${total_errors:-0}"
  else
    local summary_line
    summary_line="$(grep '^SUMMARY ' "$attack_log" | tail -n 1 || true)"
    duration_s="$(extract_summary_value "$summary_line" "duration_seconds")"
    total_sent="$(extract_summary_value "$summary_line" "total_sent")"
    total_errors="$(extract_summary_value "$summary_line" "total_errors")"
    duration_s="${duration_s:-0}"
    total_sent="${total_sent:-0}"
    total_errors="${total_errors:-0}"
  fi

  throughput="$(awk -v sent="$total_sent" -v dur="$duration_s" 'BEGIN {if (dur > 0) printf "%.3f", sent / dur; else print 0}')"

  printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" \
    "$scenario" \
    "$attack_type" \
    "$modules_csv" \
    "$broker_cpu_peak" \
    "$broker_mem_peak" \
    "$throughput" \
    "$total_connects" \
    "$total_sent" \
    "$total_errors" \
    "$duration_s" \
    "$broker_log" \
    "$broker_stats_csv" \
    "$attack_log" >>"$SUMMARY_CSV"
}

run_scenario() {
  local scenario="$1"
  local attack_type="$2"
  local modules="$3"
  local scenario_index="$4"

  local scenario_dir="$RAW_DIR/$scenario"
  local broker_log="$scenario_dir/broker.log"
  local broker_stats_csv="$scenario_dir/broker_stats.csv"
  local attack_log="$scenario_dir/attack.log"

  mkdir -p "$scenario_dir"
  echo "Running scenario=$scenario attack_type=$attack_type modules=$modules"

  configure_ports "$scenario_index"
  start_broker "$modules" "$broker_log"

  if [[ "$attack_type" == "auth" ]]; then
    run_auth_attack "$attack_log"
  else
    run_property_attack "$attack_log"
  fi

  monitor_broker_stats "$broker_stats_csv" &
  MONITOR_PID="$!"

  if ! wait "$ATTACK_PID"; then
    echo "Attack process failed for scenario $scenario. Check $attack_log" >&2
    return 1
  fi
  ATTACK_PID=""

  if [[ -n "$MONITOR_PID" ]]; then
    kill "$MONITOR_PID" 2>/dev/null || true
    wait "$MONITOR_PID" 2>/dev/null || true
    MONITOR_PID=""
  fi

  stop_broker
  append_summary_row "$scenario" "$attack_type" "$modules" "$broker_stats_csv" "$attack_log" "$broker_log"
}

build_manifest() {
  {
    echo "path,type"
    echo "summary.csv,summary"
    echo "run_info.txt,metadata"
    echo "plot_generation.log,metadata"
    echo "plots/plots_manifest.csv,metadata"
    while IFS= read -r file; do
      rel="${file#"$RESULTS_DIR/"}"
      case "$rel" in
        plots/*.png) type="plot" ;;
        *.csv) type="samples" ;;
        *.log) type="log" ;;
        *) type="artifact" ;;
      esac
      echo "$rel,$type"
    done < <(find "$RAW_DIR" "$PLOTS_DIR" -type f | sort)
  } >"$MANIFEST_CSV"
}

generate_plots() {
  if [[ ! -f "$PLOT_SCRIPT" ]]; then
    echo "Plot script not found: $PLOT_SCRIPT" >&2
    return 1
  fi
  "$PYTHON_BIN" "$PLOT_SCRIPT" \
    --summary-csv "$SUMMARY_CSV" \
    --output-dir "$PLOTS_DIR" >"$PLOT_LOG" 2>&1
  for required_file in \
    "$PLOTS_DIR/combined_auth_property_overview.png" \
    "$PLOTS_DIR/plots_manifest.csv"; do
    if [[ ! -f "$required_file" ]]; then
      echo "Missing expected plot artifact: $required_file" >&2
      echo "Plot log: $PLOT_LOG" >&2
      return 1
    fi
  done
}

RUN_STARTED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "Building binaries..."
(cd "$BROKER_DIR" && go build -o "$BROKER_BIN" ./cmd/main.go)
(cd "$ROOT_DIR" && go build -o "$AUTH_INJECTOR_BIN" ./client/auth_injector/main.go)
(cd "$ROOT_DIR" && go build -o "$PROPERTY_INJECTOR_BIN" ./client/property_injector/main.go)

ensure_plot_dependencies

run_scenario "auth_baseline" "auth" "baseline" "0"
run_scenario "auth_combined_defense" "auth" "auth-defense,property-validator" "1"
run_scenario "property_baseline" "property" "baseline" "2"
run_scenario "property_combined_defense" "property" "auth-defense,property-validator" "3"

generate_plots
build_manifest
RUN_FINISHED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cat >"$RUN_INFO_TXT" <<EOF
run_started_utc=$RUN_STARTED_UTC
run_finished_utc=$RUN_FINISHED_UTC
results_dir=$RESULTS_DIR
summary_csv=$SUMMARY_CSV
manifest_csv=$MANIFEST_CSV
plots_dir=$PLOTS_DIR
plot_generation_log=$PLOT_LOG
auth_attack_concurrency=$AUTH_ATTACK_CONCURRENCY
auth_attack_duration_s=$AUTH_ATTACK_DURATION_S
auth_attack_type=$AUTH_ATTACK_TYPE
property_attack_concurrency=$PROPERTY_ATTACK_CONCURRENCY
property_attack_duration_s=$PROPERTY_ATTACK_DURATION_S
property_attack_prop_count=$PROPERTY_ATTACK_PROP_COUNT
property_attack_key_size=$PROPERTY_ATTACK_KEY_SIZE
property_attack_val_size=$PROPERTY_ATTACK_VAL_SIZE
EOF

echo "Combined auth/property defense experiment complete."
echo "Results directory: $RESULTS_DIR"
echo "Summary CSV: $SUMMARY_CSV"
