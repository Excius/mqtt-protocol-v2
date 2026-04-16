#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RESULTS_ROOT="${RESULTS_ROOT:-$ROOT_DIR/results/module_matrix}"
RESULTS_DIR="${1:-$RESULTS_ROOT}"
RAW_DIR="$RESULTS_DIR/raw"
METADATA_DIR="$RESULTS_DIR/metadata"
BROKER_DIR="$ROOT_DIR/broker"
CERT_DIR="${TLS_CERT_DIR:-$ROOT_DIR/experiments/baseline/certs}"
GENERATE_CERTS_SCRIPT="$ROOT_DIR/experiments/baseline/generate_tls_certs.sh"

MQTT_BROKER_HOST="${MQTT_BROKER_HOST:-127.0.0.1}"
MQTT_BROKER_PORT_BASE="${MQTT_BROKER_PORT_BASE:-32883}"
MQTT_INFO_PORT_BASE="${MQTT_INFO_PORT_BASE:-32080}"
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
CONNECT_TLS_SESSION_CACHE_SIZE="${CONNECT_TLS_SESSION_CACHE_SIZE:-0}"
RECONNECT_TLS_SESSION_CACHE_SIZE="${RECONNECT_TLS_SESSION_CACHE_SIZE:-$MQTT_TLS_SESSION_CACHE_SIZE}"

VALID_CONNECT_ATTEMPTS="${VALID_CONNECT_ATTEMPTS:-150}"
VALID_CONNECT_CONCURRENCY="${VALID_CONNECT_CONCURRENCY:-12}"
RECONNECT_ATTEMPTS="${RECONNECT_ATTEMPTS:-120}"
RECONNECT_GAP_MS="${RECONNECT_GAP_MS:-20}"
RECONNECT_TICKET_WAIT_MS="${RECONNECT_TICKET_WAIT_MS:-100}"
CONNECT_TIMEOUT_MS="${CONNECT_TIMEOUT_MS:-5000}"
RECONNECT_TIMEOUT_MS="${RECONNECT_TIMEOUT_MS:-5000}"

EXPECTED_MIN_SESSION_SPEEDUP="${EXPECTED_MIN_SESSION_SPEEDUP:-1.20}"
MAX_CONNECT_DRIFT_PCT="${MAX_CONNECT_DRIFT_PCT:-20}"

SUMMARY_CSV="$RESULTS_DIR/summary.csv"
RUN_INFO_TXT="$RESULTS_DIR/run_info.txt"
VALIDATION_TXT="$RESULTS_DIR/validation.txt"
MANIFEST_CSV="$RESULTS_DIR/dataset_manifest.csv"
BROKER_PID=""

reset_results_dir() {
  mkdir -p "$RESULTS_DIR"
  rm -rf "$RAW_DIR" "$METADATA_DIR"
  rm -f "$SUMMARY_CSV" "$RUN_INFO_TXT" "$VALIDATION_TXT" "$MANIFEST_CSV"
}

reset_results_dir
mkdir -p "$RAW_DIR" "$METADATA_DIR"

cleanup() {
  if [[ -n "$BROKER_PID" ]] && kill -0 "$BROKER_PID" 2>/dev/null; then
    kill "$BROKER_PID" 2>/dev/null || true
    wait "$BROKER_PID" 2>/dev/null || true
  fi
}

trap cleanup EXIT

cat >"$SUMMARY_CSV" <<'EOF'
scenario,modules,expect_session_resumption,tls_probe_available,tls_second_reused,valid_total,valid_success,valid_failure,valid_avg_ms,valid_p95_ms,reconnect_total,reconnect_success,reconnect_failure,reconnect_avg_ms,reconnect_p95_ms,reconnect_first_avg_ms,reconnect_speedup_x,validation_status,validation_notes,valid_connect_log,reconnect_log,broker_log
EOF

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
  local broker_start=$((MQTT_BROKER_PORT_BASE + (scenario_index * 100)))
  local info_start=$((MQTT_INFO_PORT_BASE + (scenario_index * 100)))

  MQTT_BROKER_PORT="$(find_free_port "$broker_start")"
  MQTT_INFO_PORT="$(find_free_port "$info_start")"

  if [[ -z "$MQTT_BROKER_PORT" || -z "$MQTT_INFO_PORT" ]]; then
    echo "Failed to allocate ports for scenario index $scenario_index" >&2
    return 1
  fi

  MQTT_BROKER_ADDR=":${MQTT_BROKER_PORT}"
  MQTT_INFO_ADDR=":${MQTT_INFO_PORT}"
  MQTT_BROKER_URL="ssl://${MQTT_BROKER_HOST}:${MQTT_BROKER_PORT}"
  MQTT_INFO_URL="http://127.0.0.1:${MQTT_INFO_PORT}/"
}

ensure_tls_certs() {
  if [[ -f "$MQTT_TLS_CERT_FILE" && -f "$MQTT_TLS_KEY_FILE" && -f "$MQTT_TLS_CA_FILE" ]]; then
    return
  fi
  mkdir -p "$CERT_DIR"
  "$GENERATE_CERTS_SCRIPT" "$CERT_DIR"
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

  env | sort >"$env_info"
}

start_broker() {
  local modules="$1"
  local broker_log="$2"

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

run_connect_probe() {
  local out_csv="$1"
  local out_log="$2"
  local cache_size="$3"

  MQTT_BROKER_URL="$MQTT_BROKER_URL" \
    MQTT_TLS_CA_FILE="$MQTT_TLS_CA_FILE" \
    MQTT_TLS_SERVER_NAME="$MQTT_TLS_SERVER_NAME" \
    MQTT_TLS_INSECURE_SKIP_VERIFY="$MQTT_TLS_INSECURE_SKIP_VERIFY" \
    MQTT_TLS_SESSION_CACHE_SIZE="$cache_size" \
    go run ./client/probe connect \
      --broker "$MQTT_BROKER_URL" \
      --attempts "$VALID_CONNECT_ATTEMPTS" \
      --concurrency "$VALID_CONNECT_CONCURRENCY" \
      --timeout-ms "$CONNECT_TIMEOUT_MS" \
      --out "$out_csv" >"$out_log" 2>&1
}

run_reconnect_probe() {
  local out_csv="$1"
  local out_log="$2"
  local cache_size="$3"

  MQTT_BROKER_URL="$MQTT_BROKER_URL" \
    MQTT_TLS_CA_FILE="$MQTT_TLS_CA_FILE" \
    MQTT_TLS_SERVER_NAME="$MQTT_TLS_SERVER_NAME" \
    MQTT_TLS_INSECURE_SKIP_VERIFY="$MQTT_TLS_INSECURE_SKIP_VERIFY" \
    MQTT_TLS_SESSION_CACHE_SIZE="$cache_size" \
    go run ./client/probe reconnect \
      --broker "$MQTT_BROKER_URL" \
      --attempts "$RECONNECT_ATTEMPTS" \
      --timeout-ms "$RECONNECT_TIMEOUT_MS" \
      --gap-ms "$RECONNECT_GAP_MS" \
      --ticket-wait-ms "$RECONNECT_TICKET_WAIT_MS" \
      --out "$out_csv" >"$out_log" 2>&1
}

parse_connect_log() {
  local log_file="$1"
  local line total success failure avg p95
  line="$(grep 'SUMMARY mode=connect' "$log_file" | tail -n 1 || true)"
  total="$(extract_summary_value "$line" "total")"
  success="$(extract_summary_value "$line" "success")"
  failure="$(extract_summary_value "$line" "failure")"
  avg="$(extract_summary_value "$line" "avg_ms")"
  p95="$(extract_summary_value "$line" "p95_ms")"
  echo "${total:-0}|${success:-0}|${failure:-0}|${avg:-0}|${p95:-0}"
}

parse_reconnect_log() {
  local log_file="$1"
  local summary_line resumption_line total success failure avg p95 first_avg speedup
  summary_line="$(grep 'SUMMARY mode=reconnect' "$log_file" | tail -n 1 || true)"
  resumption_line="$(grep 'RESUMPTION ' "$log_file" | tail -n 1 || true)"
  total="$(extract_summary_value "$summary_line" "total")"
  success="$(extract_summary_value "$summary_line" "success")"
  failure="$(extract_summary_value "$summary_line" "failure")"
  avg="$(extract_summary_value "$summary_line" "avg_ms")"
  p95="$(extract_summary_value "$summary_line" "p95_ms")"
  first_avg="$(extract_summary_value "$resumption_line" "first_avg_ms")"
  speedup="$(extract_summary_value "$resumption_line" "speedup_x")"
  echo "${total:-0}|${success:-0}|${failure:-0}|${avg:-0}|${p95:-0}|${first_avg:-0}|${speedup:-0}"
}

run_tls_probe() {
  local scenario_dir="$1"
  local probe_csv="$scenario_dir/tls_session_probe.csv"
  local first_log="$scenario_dir/tls_session_probe_first.log"
  local second_log="$scenario_dir/tls_session_probe_second.log"
  local sess_file="$scenario_dir/tls_session_probe.sess"
  local openssl_available="false"
  local reused="na"

  if command -v openssl >/dev/null 2>&1; then
    openssl_available="true"
    reused="false"

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
        reused="true"
      fi
    else
      : >"$second_log"
    fi
  else
    : >"$first_log"
    : >"$second_log"
  fi

  {
    echo "probe_key,probe_value"
    echo "openssl_available,$openssl_available"
    echo "tls_second_reused,$reused"
  } >"$probe_csv"

  echo "$openssl_available|$reused|$probe_csv|$first_log|$second_log"
}

float_ge() {
  local left="$1"
  local right="$2"
  awk -v l="$left" -v r="$right" 'BEGIN {exit (l >= r) ? 0 : 1}'
}

abs_percent_delta() {
  local left="$1"
  local right="$2"
  awk -v l="$left" -v r="$right" 'BEGIN {
    if (l == 0) {
      print 0
      exit
    }
    d = ((l - r) / l) * 100
    if (d < 0) d = -d
    printf "%.3f", d
  }'
}

validate_scenario() {
  local expect_session="$1"
  local tls_probe_available="$2"
  local tls_second_reused="$3"
  local valid_total="$4"
  local valid_success="$5"
  local reconnect_total="$6"
  local reconnect_success="$7"
  local reconnect_speedup="$8"

  local status="pass"
  local notes=()

  if [[ "$valid_success" != "$valid_total" ]]; then
    status="fail"
    notes+=("valid_connect_not_all_success")
  fi
  if [[ "$reconnect_success" != "$reconnect_total" ]]; then
    status="fail"
    notes+=("reconnect_not_all_success")
  fi

  if [[ "$expect_session" == "true" ]]; then
    if [[ "$tls_probe_available" == "true" && "$tls_second_reused" != "true" ]]; then
      status="fail"
      notes+=("openssl_session_not_reused")
    fi
    if ! float_ge "$reconnect_speedup" "$EXPECTED_MIN_SESSION_SPEEDUP"; then
      status="fail"
      notes+=("session_speedup_below_${EXPECTED_MIN_SESSION_SPEEDUP}")
    fi
  else
    if [[ "$tls_probe_available" == "true" && "$tls_second_reused" != "false" ]]; then
      status="fail"
      notes+=("unexpected_tls_session_reuse")
    fi
  fi

  if [[ "${#notes[@]}" -eq 0 ]]; then
    echo "$status|ok"
    return 0
  fi

  local joined
  joined="$(IFS=';'; echo "${notes[*]}")"
  echo "$status|$joined"
}

run_scenario() {
  local scenario="$1"
  local modules="$2"
  local expect_session="$3"
  local scenario_index="$4"

  local scenario_dir="$RAW_DIR/$scenario"
  local broker_log="$scenario_dir/broker.log"
  local valid_connect_csv="$scenario_dir/valid_connect.csv"
  local valid_connect_log="$scenario_dir/valid_connect.log"
  local reconnect_csv="$scenario_dir/reconnect.csv"
  local reconnect_log="$scenario_dir/reconnect.log"
  local tls_probe_info tls_probe_available tls_second_reused tls_probe_csv tls_probe_first_log tls_probe_second_log
  local valid_stats reconnect_stats validation status notes
  local valid_total valid_success valid_failure valid_avg valid_p95
  local reconnect_total reconnect_success reconnect_failure reconnect_avg reconnect_p95 reconnect_first reconnect_speedup

  mkdir -p "$scenario_dir"
  configure_ports "$scenario_index"
  start_broker "$modules" "$broker_log"

  run_connect_probe "$valid_connect_csv" "$valid_connect_log" "$CONNECT_TLS_SESSION_CACHE_SIZE"
  run_reconnect_probe "$reconnect_csv" "$reconnect_log" "$RECONNECT_TLS_SESSION_CACHE_SIZE"

  tls_probe_info="$(run_tls_probe "$scenario_dir")"
  IFS='|' read -r tls_probe_available tls_second_reused tls_probe_csv tls_probe_first_log tls_probe_second_log <<<"$tls_probe_info"

  stop_broker

  valid_stats="$(parse_connect_log "$valid_connect_log")"
  reconnect_stats="$(parse_reconnect_log "$reconnect_log")"
  IFS='|' read -r valid_total valid_success valid_failure valid_avg valid_p95 <<<"$valid_stats"
  IFS='|' read -r reconnect_total reconnect_success reconnect_failure reconnect_avg reconnect_p95 reconnect_first reconnect_speedup <<<"$reconnect_stats"

  validation="$(validate_scenario \
    "$expect_session" \
    "$tls_probe_available" \
    "$tls_second_reused" \
    "$valid_total" \
    "$valid_success" \
    "$reconnect_total" \
    "$reconnect_success" \
    "$reconnect_speedup")"
  IFS='|' read -r status notes <<<"$validation"

  printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" \
    "$scenario" \
    "$modules" \
    "$expect_session" \
    "$tls_probe_available" \
    "$tls_second_reused" \
    "$valid_total" \
    "$valid_success" \
    "$valid_failure" \
    "$valid_avg" \
    "$valid_p95" \
    "$reconnect_total" \
    "$reconnect_success" \
    "$reconnect_failure" \
    "$reconnect_avg" \
    "$reconnect_p95" \
    "$reconnect_first" \
    "$reconnect_speedup" \
    "$status" \
    "$notes" \
    "$valid_connect_log" \
    "$reconnect_log" \
    "$broker_log" >>"$SUMMARY_CSV"
}

build_manifest() {
  {
    echo "path,type"
    echo "summary.csv,summary"
    echo "validation.txt,summary"
    echo "run_info.txt,metadata"
    echo "metadata/host_info.csv,metadata"
    echo "metadata/git_info.txt,metadata"
    echo "metadata/environment.txt,metadata"
    while IFS= read -r file; do
      rel="${file#"$RESULTS_DIR/"}"
      case "$rel" in
        *.csv) type="samples" ;;
        *.log) type="log" ;;
        *.sess) type="artifact" ;;
        *) type="artifact" ;;
      esac
      echo "$rel,$type"
    done < <(find "$RAW_DIR" -type f | sort)
  } >"$MANIFEST_CSV"
}

RUN_STARTED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

ensure_tls_certs
capture_metadata

run_scenario "baseline" "baseline" "false" "0"
run_scenario "session_only" "tls-session-resumption" "true" "1"

BASELINE_FIRST_CONNECT_AVG="$(awk -F, '$1=="baseline"{print $16}' "$SUMMARY_CSV")"
SESSION_ONLY_FIRST_CONNECT_AVG="$(awk -F, '$1=="session_only"{print $16}' "$SUMMARY_CSV")"
FIRST_CONNECT_SESSION_DRIFT_PCT="$(abs_percent_delta "$BASELINE_FIRST_CONNECT_AVG" "$SESSION_ONLY_FIRST_CONNECT_AVG")"

FAILED_COUNT="$(awk -F, 'NR > 1 && $18 != "pass" {count++} END {print count + 0}' "$SUMMARY_CSV")"
TOTAL_SCENARIOS="$(awk 'END {print NR - 1}' "$SUMMARY_CSV")"
OVERALL_STATUS="pass"
MATRIX_NOTES=()

if [[ "$FAILED_COUNT" -gt 0 ]]; then
  OVERALL_STATUS="fail"
fi
if ! float_ge "$MAX_CONNECT_DRIFT_PCT" "$FIRST_CONNECT_SESSION_DRIFT_PCT"; then
  OVERALL_STATUS="fail"
  MATRIX_NOTES+=("session_only_first_connect_drift_${FIRST_CONNECT_SESSION_DRIFT_PCT}_gt_${MAX_CONNECT_DRIFT_PCT}")
fi

{
  echo "overall_status=$OVERALL_STATUS"
  echo "failed_scenarios=$FAILED_COUNT"
  echo "total_scenarios=$TOTAL_SCENARIOS"
  echo "expected_min_session_speedup=$EXPECTED_MIN_SESSION_SPEEDUP"
  echo "baseline_first_connect_avg_ms=$BASELINE_FIRST_CONNECT_AVG"
  echo "session_only_first_connect_avg_ms=$SESSION_ONLY_FIRST_CONNECT_AVG"
  echo "first_connect_session_drift_pct=$FIRST_CONNECT_SESSION_DRIFT_PCT"
  echo "max_connect_drift_pct=$MAX_CONNECT_DRIFT_PCT"
  if [[ "${#MATRIX_NOTES[@]}" -eq 0 ]]; then
    echo "matrix_notes=ok"
  else
    echo "matrix_notes=$(IFS=';'; echo "${MATRIX_NOTES[*]}")"
  fi
} >"$VALIDATION_TXT"

build_manifest

RUN_FINISHED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cat >"$RUN_INFO_TXT" <<EOF
run_started_utc=$RUN_STARTED_UTC
run_finished_utc=$RUN_FINISHED_UTC
results_root=$RESULTS_ROOT
results_dir=$RESULTS_DIR
summary_csv=$SUMMARY_CSV
validation_file=$VALIDATION_TXT
manifest_csv=$MANIFEST_CSV
mqtt_tls_cert_file=$MQTT_TLS_CERT_FILE
mqtt_tls_key_file=$MQTT_TLS_KEY_FILE
mqtt_tls_ca_file=$MQTT_TLS_CA_FILE
mqtt_tls_server_name=$MQTT_TLS_SERVER_NAME
mqtt_tls_session_cache_size=$MQTT_TLS_SESSION_CACHE_SIZE
connect_tls_session_cache_size=$CONNECT_TLS_SESSION_CACHE_SIZE
reconnect_tls_session_cache_size=$RECONNECT_TLS_SESSION_CACHE_SIZE
valid_connect_attempts=$VALID_CONNECT_ATTEMPTS
reconnect_attempts=$RECONNECT_ATTEMPTS
reconnect_gap_ms=$RECONNECT_GAP_MS
reconnect_ticket_wait_ms=$RECONNECT_TICKET_WAIT_MS
expected_min_session_speedup=$EXPECTED_MIN_SESSION_SPEEDUP
max_connect_drift_pct=$MAX_CONNECT_DRIFT_PCT
overall_status=$OVERALL_STATUS
failed_scenarios=$FAILED_COUNT
EOF

echo "Module matrix benchmark complete."
echo "Results directory: $RESULTS_DIR"
echo "Summary CSV: $SUMMARY_CSV"
echo "Validation status: $OVERALL_STATUS"

if [[ "$OVERALL_STATUS" != "pass" ]]; then
  exit 1
fi
