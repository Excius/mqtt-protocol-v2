#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RESULTS_ROOT="${RESULTS_ROOT:-$ROOT_DIR/results/tls_resumption}"
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

PYTHON_BIN="${PYTHON_BIN:-python3}"
BROKER_DIR="$ROOT_DIR/broker"
PLOT_SCRIPT="$ROOT_DIR/experiments/tls_resumption/plot_tls_resumption.py"
CERT_DIR="${TLS_CERT_DIR:-$ROOT_DIR/experiments/baseline/certs}"
GENERATE_CERTS_SCRIPT="$ROOT_DIR/experiments/baseline/generate_tls_certs.sh"

MQTT_BROKER_HOST="${MQTT_BROKER_HOST:-127.0.0.1}"
MQTT_BROKER_PORT_BASE="${MQTT_BROKER_PORT_BASE:-28883}"
MQTT_INFO_PORT_BASE="${MQTT_INFO_PORT_BASE:-28080}"
MQTT_BROKER_PORT=""
MQTT_INFO_PORT=""
MQTT_BROKER_ADDR=""
MQTT_INFO_ADDR=""
MQTT_BROKER_URL=""
MQTT_INFO_URL=""

MQTT_TLS_CERT_FILE="${MQTT_TLS_CERT_FILE:-$CERT_DIR/server.cert.pem}"
MQTT_TLS_KEY_FILE="${MQTT_TLS_KEY_FILE:-$CERT_DIR/server.key.pem}"
MQTT_TLS_CA_FILE="${MQTT_TLS_CA_FILE:-$CERT_DIR/ca.cert.pem}"
MQTT_TLS_SERVER_NAME="${MQTT_TLS_SERVER_NAME:-localhost}"
MQTT_TLS_INSECURE_SKIP_VERIFY="${MQTT_TLS_INSECURE_SKIP_VERIFY:-false}"
MQTT_TLS_SESSION_CACHE_SIZE="${MQTT_TLS_SESSION_CACHE_SIZE:-100}"
MQTT_BROKER_MODULES="${MQTT_BROKER_MODULES:-}"
CONNECT_TLS_SESSION_CACHE_SIZE="${CONNECT_TLS_SESSION_CACHE_SIZE:-0}"
RECONNECT_TLS_SESSION_CACHE_SIZE="${RECONNECT_TLS_SESSION_CACHE_SIZE:-$MQTT_TLS_SESSION_CACHE_SIZE}"

CONNECT_ATTEMPTS="${CONNECT_ATTEMPTS:-300}"
CONNECT_CONCURRENCY="${CONNECT_CONCURRENCY:-20}"
RECONNECT_ATTEMPTS="${RECONNECT_ATTEMPTS:-300}"
RECONNECT_GAP_MS="${RECONNECT_GAP_MS:-20}"
RECONNECT_TICKET_WAIT_MS="${RECONNECT_TICKET_WAIT_MS:-100}"
CONNECT_TIMEOUT_MS="${CONNECT_TIMEOUT_MS:-5000}"
RECONNECT_TIMEOUT_MS="${RECONNECT_TIMEOUT_MS:-5000}"

SUMMARY_CSV="$RESULTS_DIR/summary.csv"
RUN_INFO_TXT="$RESULTS_DIR/run_info.txt"
MANIFEST_CSV="$RESULTS_DIR/dataset_manifest.csv"
PLOT_LOG="$RESULTS_DIR/plot_generation.log"
BROKER_PID=""

reset_results_dir() {
  mkdir -p "$RESULTS_DIR"
  rm -rf "$RAW_DIR" "$PLOTS_DIR" "$METADATA_DIR"
  rm -f "$SUMMARY_CSV" "$RUN_INFO_TXT" "$MANIFEST_CSV" "$PLOT_LOG"
}

reset_results_dir
mkdir -p "$RAW_DIR" "$PLOTS_DIR" "$METADATA_DIR"

export MQTT_BROKER_URL
export MQTT_TLS_CA_FILE
export MQTT_TLS_SERVER_NAME
export MQTT_TLS_INSECURE_SKIP_VERIFY
export MQTT_TLS_SESSION_CACHE_SIZE

cat >"$SUMMARY_CSV" <<'EOF'
scenario,resumption_enabled,tls_second_reused,connect_avg_ms,connect_p50_ms,connect_p95_ms,connect_p99_ms,reconnect_avg_ms,reconnect_p50_ms,reconnect_p95_ms,reconnect_p99_ms,reconnect_first_avg_ms,reconnect_speedup_x,connect_csv,reconnect_csv,connect_log,reconnect_log,tls_probe_csv,tls_probe_first_log,tls_probe_second_log,broker_log
EOF

cleanup() {
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

scenario_modules() {
  local resumption="$1"
  if [[ -n "$MQTT_BROKER_MODULES" ]]; then
    echo "$MQTT_BROKER_MODULES"
    return 0
  fi

  if [[ "$resumption" == "true" ]]; then
    echo "tls-session-resumption"
  else
    echo "baseline"
  fi
}

ensure_tls_certs() {
  if [[ -f "$MQTT_TLS_CERT_FILE" && -f "$MQTT_TLS_KEY_FILE" && -f "$MQTT_TLS_CA_FILE" ]]; then
    return
  fi

  mkdir -p "$CERT_DIR"
  "$GENERATE_CERTS_SCRIPT" "$CERT_DIR"
}

ensure_plot_dependencies() {
  if ! "$PYTHON_BIN" -c 'import matplotlib' >/dev/null 2>&1; then
    echo "matplotlib is required for plot generation. Install it with: $PYTHON_BIN -m pip install matplotlib" >&2
    return 1
  fi
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
  local broker_start=$((MQTT_BROKER_PORT_BASE + (scenario_index * 100)))
  local info_start=$((MQTT_INFO_PORT_BASE + (scenario_index * 100)))

  MQTT_BROKER_PORT="$(find_free_port "$broker_start")"
  if [[ -z "$MQTT_BROKER_PORT" ]]; then
    echo "Failed to find a free broker port near $broker_start" >&2
    return 1
  fi

  MQTT_INFO_PORT="$(find_free_port "$info_start")"
  if [[ -z "$MQTT_INFO_PORT" ]]; then
    echo "Failed to find a free info port near $info_start" >&2
    return 1
  fi

  MQTT_BROKER_ADDR=":${MQTT_BROKER_PORT}"
  MQTT_INFO_ADDR=":${MQTT_INFO_PORT}"
  MQTT_BROKER_URL="ssl://${MQTT_BROKER_HOST}:${MQTT_BROKER_PORT}"
  MQTT_INFO_URL="http://127.0.0.1:${MQTT_INFO_PORT}/"
  export MQTT_BROKER_URL
}

capture_metadata() {
  local host_info="$METADATA_DIR/host_info.csv"
  local git_info="$METADATA_DIR/git_info.txt"
  local env_info="$METADATA_DIR/environment.txt"

  {
    echo "key,value"
    echo "captured_utc,$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "hostname,$(hostname)"
    echo "kernel,$(uname -r)"
    echo "os,$(uname -s)"
    echo "arch,$(uname -m)"
    echo "go_version,$(go version | sed 's/,/;/g')"
    echo "cpu_model,$(awk -F: '/model name/ {gsub(/^ /, "", $2); print $2; exit}' /proc/cpuinfo | sed 's/,/;/g')"
    echo "cpu_cores,$(nproc)"
    echo "mem_total_kb,$(awk '/MemTotal/ {print $2}' /proc/meminfo)"
  } >"$host_info"

  {
    echo "captured_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "root_dir=$ROOT_DIR"
    echo "git_branch=$(git -C "$ROOT_DIR" branch --show-current 2>/dev/null || true)"
    echo "git_commit=$(git -C "$ROOT_DIR" rev-parse HEAD 2>/dev/null || true)"
    echo "git_status_short:"
    git -C "$ROOT_DIR" status --short || true
  } >"$git_info"

  {
    env | sort
  } >"$env_info"
}

start_broker() {
  local resumption="$1"
  local broker_log="$2"
  local modules
  modules="$(scenario_modules "$resumption")"

  local broker_cmd=(
    go run ./cmd/main.go
    --tcp "$MQTT_BROKER_ADDR"
    --info "$MQTT_INFO_ADDR"
    --tls-cert-file "$MQTT_TLS_CERT_FILE"
    --tls-key-file "$MQTT_TLS_KEY_FILE"
    --modules "$modules"
  )

  (
    cd "$BROKER_DIR"
    exec "${broker_cmd[@]}" >"$broker_log" 2>&1
  ) &
  BROKER_PID="$!"

  for _ in $(seq 1 120); do
    if ! kill -0 "$BROKER_PID" 2>/dev/null; then
      echo "Broker process exited before ready state." >&2
      return 1
    fi

    if curl -sf --max-time 0.3 "$MQTT_INFO_URL" >/dev/null 2>&1; then
      if command -v ss >/dev/null 2>&1; then
        local listener_pid
        listener_pid="$(ss -ltnp "sport = :$MQTT_BROKER_PORT" 2>/dev/null | awk -F'pid=' 'NR > 1 && NF > 1 {split($2, a, ","); print a[1]; exit}')"
        if [[ -z "$listener_pid" ]]; then
          sleep 0.2
          continue
        fi
        BROKER_PID="$listener_pid"
      fi
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

append_summary_row() {
  local scenario="$1"
  local resumption="$2"
  local tls_second_reused="$3"
  local connect_csv="$4"
  local reconnect_csv="$5"
  local connect_log="$6"
  local reconnect_log="$7"
  local tls_probe_csv="$8"
  local tls_probe_first_log="$9"
  local tls_probe_second_log="${10}"
  local broker_log="${11}"

  local connect_line reconnect_line resumption_line
  local connect_avg connect_p50 connect_p95 connect_p99
  local reconnect_avg reconnect_p50 reconnect_p95 reconnect_p99
  local reconnect_first_avg reconnect_speedup

  connect_line="$(grep 'SUMMARY mode=connect' "$connect_log" | tail -n 1 || true)"
  reconnect_line="$(grep 'SUMMARY mode=reconnect' "$reconnect_log" | tail -n 1 || true)"
  resumption_line="$(grep 'RESUMPTION ' "$reconnect_log" | tail -n 1 || true)"

  connect_avg="$(extract_summary_value "$connect_line" "avg_ms")"
  connect_p50="$(extract_summary_value "$connect_line" "p50_ms")"
  connect_p95="$(extract_summary_value "$connect_line" "p95_ms")"
  connect_p99="$(extract_summary_value "$connect_line" "p99_ms")"

  reconnect_avg="$(extract_summary_value "$reconnect_line" "avg_ms")"
  reconnect_p50="$(extract_summary_value "$reconnect_line" "p50_ms")"
  reconnect_p95="$(extract_summary_value "$reconnect_line" "p95_ms")"
  reconnect_p99="$(extract_summary_value "$reconnect_line" "p99_ms")"

  reconnect_first_avg="$(extract_summary_value "$resumption_line" "first_avg_ms")"
  reconnect_speedup="$(extract_summary_value "$resumption_line" "speedup_x")"

  connect_avg="${connect_avg:-0}"
  connect_p50="${connect_p50:-0}"
  connect_p95="${connect_p95:-0}"
  connect_p99="${connect_p99:-0}"
  reconnect_avg="${reconnect_avg:-0}"
  reconnect_p50="${reconnect_p50:-0}"
  reconnect_p95="${reconnect_p95:-0}"
  reconnect_p99="${reconnect_p99:-0}"
  reconnect_first_avg="${reconnect_first_avg:-0}"
  reconnect_speedup="${reconnect_speedup:-0}"

  printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" \
    "$scenario" \
    "$resumption" \
    "$tls_second_reused" \
    "$connect_avg" \
    "$connect_p50" \
    "$connect_p95" \
    "$connect_p99" \
    "$reconnect_avg" \
    "$reconnect_p50" \
    "$reconnect_p95" \
    "$reconnect_p99" \
    "$reconnect_first_avg" \
    "$reconnect_speedup" \
    "$connect_csv" \
    "$reconnect_csv" \
    "$connect_log" \
    "$reconnect_log" \
    "$tls_probe_csv" \
    "$tls_probe_first_log" \
    "$tls_probe_second_log" \
    "$broker_log" >>"$SUMMARY_CSV"
}

run_tls_probe() {
  local scenario_dir="$1"
  local probe_csv="$scenario_dir/tls_session_probe.csv"
  local first_log="$scenario_dir/tls_session_probe_first.log"
  local second_log="$scenario_dir/tls_session_probe_second.log"
  local sess_file="$scenario_dir/tls_session_probe.sess"
  local second_reused="false"

  if command -v openssl >/dev/null 2>&1; then
    printf '' | openssl s_client \
      -connect "${MQTT_BROKER_HOST}:${MQTT_BROKER_PORT}" \
      -servername "$MQTT_TLS_SERVER_NAME" \
      -CAfile "$MQTT_TLS_CA_FILE" \
      -sess_out "$sess_file" >"$first_log" 2>&1 || true

    sleep 0.2

    if [[ -f "$sess_file" ]]; then
      printf '' | openssl s_client \
        -connect "${MQTT_BROKER_HOST}:${MQTT_BROKER_PORT}" \
        -servername "$MQTT_TLS_SERVER_NAME" \
        -CAfile "$MQTT_TLS_CA_FILE" \
        -sess_in "$sess_file" >"$second_log" 2>&1 || true

      if grep -q '^Reused, TLS' "$second_log"; then
        second_reused="true"
      fi
    else
      : >"$second_log"
    fi
  fi

  {
    echo "probe_key,probe_value"
    echo "tls_second_reused,$second_reused"
  } >"$probe_csv"

  echo "$second_reused|$probe_csv|$first_log|$second_log"
}

run_scenario() {
  local scenario="$1"
  local resumption="$2"
  local scenario_index="$3"
  local scenario_dir="$RAW_DIR/$scenario"
  local broker_log="$scenario_dir/broker.log"
  local connect_csv="$scenario_dir/connect_latency.csv"
  local connect_log="$scenario_dir/connect_latency.log"
  local reconnect_csv="$scenario_dir/reconnect_latency.csv"
  local reconnect_log="$scenario_dir/reconnect_latency.log"
  local tls_probe_output tls_second_reused tls_probe_csv tls_probe_first_log tls_probe_second_log

  mkdir -p "$scenario_dir"

  echo "Running scenario=$scenario tls_session_resumption=$resumption"
  configure_ports "$scenario_index"
  start_broker "$resumption" "$broker_log"

  (
    cd "$ROOT_DIR"
    MQTT_TLS_SESSION_CACHE_SIZE="$CONNECT_TLS_SESSION_CACHE_SIZE" \
      go run ./client/probe connect \
        --broker "$MQTT_BROKER_URL" \
        --attempts "$CONNECT_ATTEMPTS" \
        --concurrency "$CONNECT_CONCURRENCY" \
        --timeout-ms "$CONNECT_TIMEOUT_MS" \
        --out "$connect_csv"
  ) >"$connect_log" 2>&1

  (
    cd "$ROOT_DIR"
    MQTT_TLS_SESSION_CACHE_SIZE="$RECONNECT_TLS_SESSION_CACHE_SIZE" \
      go run ./client/probe reconnect \
        --broker "$MQTT_BROKER_URL" \
        --attempts "$RECONNECT_ATTEMPTS" \
        --timeout-ms "$RECONNECT_TIMEOUT_MS" \
        --gap-ms "$RECONNECT_GAP_MS" \
        --ticket-wait-ms "$RECONNECT_TICKET_WAIT_MS" \
        --out "$reconnect_csv"
  ) >"$reconnect_log" 2>&1

  tls_probe_output="$(run_tls_probe "$scenario_dir")"
  IFS='|' read -r tls_second_reused tls_probe_csv tls_probe_first_log tls_probe_second_log <<<"$tls_probe_output"

  stop_broker
  append_summary_row "$scenario" "$resumption" "$tls_second_reused" "$connect_csv" "$reconnect_csv" "$connect_log" "$reconnect_log" "$tls_probe_csv" "$tls_probe_first_log" "$tls_probe_second_log" "$broker_log"
}

RUN_STARTED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
OLD_SCENARIO_MODULES="$(scenario_modules false)"
NEW_SCENARIO_MODULES="$(scenario_modules true)"

ensure_tls_certs
ensure_plot_dependencies
capture_metadata

run_scenario "old_no_resumption" "false" "0"
run_scenario "new_with_resumption" "true" "1"

if [[ ! -f "$PLOT_SCRIPT" ]]; then
  echo "Plot script not found: $PLOT_SCRIPT" >&2
  exit 1
fi

"$PYTHON_BIN" "$PLOT_SCRIPT" \
  --summary-csv "$SUMMARY_CSV" \
  --raw-dir "$RAW_DIR" \
  --output-dir "$PLOTS_DIR" >"$PLOT_LOG" 2>&1

for required_file in \
  "$PLOTS_DIR/reconnect_cdf.png" \
  "$PLOTS_DIR/avg_connect_reconnect.png" \
  "$PLOTS_DIR/reconnect_speedup.png" \
  "$PLOTS_DIR/plots_manifest.csv"; do
  if [[ ! -f "$required_file" ]]; then
    echo "Missing expected plot artifact: $required_file" >&2
    echo "Plot log: $PLOT_LOG" >&2
    exit 1
  fi
done

cat >"$MANIFEST_CSV" <<'EOF'
path,type
run_info.txt,metadata
summary.csv,summary
metadata/host_info.csv,metadata
metadata/git_info.txt,metadata
metadata/environment.txt,metadata
raw/old_no_resumption/connect_latency.csv,samples
raw/old_no_resumption/reconnect_latency.csv,samples
raw/old_no_resumption/connect_latency.log,log
raw/old_no_resumption/reconnect_latency.log,log
raw/old_no_resumption/broker.log,log
raw/old_no_resumption/tls_session_probe.csv,summary
raw/old_no_resumption/tls_session_probe_first.log,log
raw/old_no_resumption/tls_session_probe_second.log,log
raw/new_with_resumption/connect_latency.csv,samples
raw/new_with_resumption/reconnect_latency.csv,samples
raw/new_with_resumption/connect_latency.log,log
raw/new_with_resumption/reconnect_latency.log,log
raw/new_with_resumption/broker.log,log
raw/new_with_resumption/tls_session_probe.csv,summary
raw/new_with_resumption/tls_session_probe_first.log,log
raw/new_with_resumption/tls_session_probe_second.log,log
plots/reconnect_cdf.png,plot
plots/avg_connect_reconnect.png,plot
plots/reconnect_speedup.png,plot
plots/plots_manifest.csv,metadata
plot_generation.log,metadata
EOF

RUN_FINISHED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cat >"$RUN_INFO_TXT" <<EOF
run_started_utc=$RUN_STARTED_UTC
run_finished_utc=$RUN_FINISHED_UTC
results_root=$RESULTS_ROOT
results_dir=$RESULTS_DIR
summary_csv=$SUMMARY_CSV
manifest_csv=$MANIFEST_CSV
plot_generation_log=$PLOT_LOG
mqtt_broker_url=$MQTT_BROKER_URL
mqtt_info_url=$MQTT_INFO_URL
mqtt_tls_cert_file=$MQTT_TLS_CERT_FILE
mqtt_tls_key_file=$MQTT_TLS_KEY_FILE
mqtt_tls_ca_file=$MQTT_TLS_CA_FILE
mqtt_tls_server_name=$MQTT_TLS_SERVER_NAME
mqtt_tls_session_cache_size=$MQTT_TLS_SESSION_CACHE_SIZE
connect_tls_session_cache_size=$CONNECT_TLS_SESSION_CACHE_SIZE
reconnect_tls_session_cache_size=$RECONNECT_TLS_SESSION_CACHE_SIZE
mqtt_broker_modules_override=$MQTT_BROKER_MODULES
old_no_resumption_modules=$OLD_SCENARIO_MODULES
new_with_resumption_modules=$NEW_SCENARIO_MODULES
mqtt_broker_port_base=$MQTT_BROKER_PORT_BASE
mqtt_info_port_base=$MQTT_INFO_PORT_BASE
connect_attempts=$CONNECT_ATTEMPTS
connect_concurrency=$CONNECT_CONCURRENCY
reconnect_attempts=$RECONNECT_ATTEMPTS
reconnect_gap_ms=$RECONNECT_GAP_MS
reconnect_ticket_wait_ms=$RECONNECT_TICKET_WAIT_MS
EOF

echo "TLS resumption comparison complete."
echo "Results directory: $RESULTS_DIR"
echo "Summary CSV: $SUMMARY_CSV"
