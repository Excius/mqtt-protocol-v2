#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RESULTS_ROOT="${RESULTS_ROOT:-$ROOT_DIR/results/tls_profiles}"
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
PLOT_SCRIPT="$ROOT_DIR/experiments/tls_profiles/plot_tls_profiles.py"
CERT_DIR="${TLS_CERT_DIR:-$ROOT_DIR/experiments/baseline/certs}"
GENERATE_CERTS_SCRIPT="$ROOT_DIR/experiments/baseline/generate_tls_certs.sh"
LOAD_CLIENT_BIN="$BIN_DIR/load_client"
BROKER_BIN="$BIN_DIR/broker"

MQTT_BROKER_HOST="${MQTT_BROKER_HOST:-127.0.0.1}"
MQTT_BROKER_PORT_BASE="${MQTT_BROKER_PORT_BASE:-38883}"
MQTT_WS_PORT_BASE="${MQTT_WS_PORT_BASE:-38882}"
MQTT_INFO_PORT_BASE="${MQTT_INFO_PORT_BASE:-38080}"
MQTT_BROKER_PORT=""
MQTT_WS_PORT=""
MQTT_INFO_PORT=""
MQTT_BROKER_ADDR=""
MQTT_WS_ADDR=""
MQTT_INFO_ADDR=""
MQTT_BROKER_URL=""
MQTT_INFO_URL=""

MQTT_TLS_CERT_FILE="${MQTT_TLS_CERT_FILE:-$CERT_DIR/server.cert.pem}"
MQTT_TLS_KEY_FILE="${MQTT_TLS_KEY_FILE:-$CERT_DIR/server.key.pem}"
MQTT_TLS_CA_FILE="${MQTT_TLS_CA_FILE:-$CERT_DIR/ca.cert.pem}"
MQTT_TLS_SERVER_NAME="${MQTT_TLS_SERVER_NAME:-localhost}"
MQTT_TLS_INSECURE_SKIP_VERIFY="${MQTT_TLS_INSECURE_SKIP_VERIFY:-false}"

TLS_PROFILE_MODULES="${TLS_PROFILE_MODULES:-adaptive-tls-profiles}"
TLS_PROFILE_EXTRA_MODULES="${TLS_PROFILE_EXTRA_MODULES:-}"
TLS_PROFILES="${TLS_PROFILES:-LOW_POWER,BALANCED,HIGH_SECURITY}"

CONNECT_ATTEMPTS="${CONNECT_ATTEMPTS:-400}"
CONNECT_CONCURRENCY="${CONNECT_CONCURRENCY:-40}"
CONNECT_TIMEOUT_MS="${CONNECT_TIMEOUT_MS:-5000}"
CONNECT_TLS_SESSION_CACHE_SIZE="${CONNECT_TLS_SESSION_CACHE_SIZE:-0}"

LOAD_WORKERS="${LOAD_WORKERS:-150}"
LOAD_MESSAGES_PER_WORKER="${LOAD_MESSAGES_PER_WORKER:-12000}"
LOAD_DELAY_MS="${LOAD_DELAY_MS:-5}"
LOAD_TLS_SESSION_CACHE_SIZE="${LOAD_TLS_SESSION_CACHE_SIZE:-0}"
LOAD_RECONNECT_EVERY="${LOAD_RECONNECT_EVERY:-200}"
STATS_SAMPLE_INTERVAL_MS="${STATS_SAMPLE_INTERVAL_MS:-200}"
SETTLE_DELAY_S="${SETTLE_DELAY_S:-2}"
STATS_WARMUP_SKIP="${STATS_WARMUP_SKIP:-3}"

BROKER_PID=""
LOAD_PID=""
BROKER_MONITOR_PID=""
CLIENT_MONITOR_PID=""
CLK_TCK="$(getconf CLK_TCK 2>/dev/null || echo 100)"

mkdir -p "$RESULTS_DIR"
rm -rf "$RAW_DIR" "$PLOTS_DIR" "$METADATA_DIR" "$BIN_DIR"
rm -f "$SUMMARY_CSV" "$RUN_INFO_TXT" "$MANIFEST_CSV" "$PLOT_LOG"
mkdir -p "$RAW_DIR" "$PLOTS_DIR" "$METADATA_DIR" "$BIN_DIR"

cat >"$SUMMARY_CSV" <<'EOF'
profile,modules,negotiated_protocol,negotiated_cipher,negotiated_key_exchange,handshake_avg_ms,handshake_p95_ms,broker_cpu_avg_pct,broker_cpu_peak_pct,broker_mem_avg_mib,broker_mem_peak_mib,client_cpu_avg_pct,client_cpu_peak_pct,client_mem_avg_mib,client_mem_peak_mib,throughput_msgs_per_s,total_publishes,connect_errors,publish_errors,load_duration_seconds,connect_csv,connect_log,load_log,broker_log,broker_stats_csv,client_stats_csv,tls_probe_csv,tls_probe_log
EOF

cleanup() {
  if [[ -n "$BROKER_MONITOR_PID" ]] && kill -0 "$BROKER_MONITOR_PID" 2>/dev/null; then
    kill "$BROKER_MONITOR_PID" 2>/dev/null || true
    wait "$BROKER_MONITOR_PID" 2>/dev/null || true
  fi
  if [[ -n "$CLIENT_MONITOR_PID" ]] && kill -0 "$CLIENT_MONITOR_PID" 2>/dev/null; then
    kill "$CLIENT_MONITOR_PID" 2>/dev/null || true
    wait "$CLIENT_MONITOR_PID" 2>/dev/null || true
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
  local ws_start=$((MQTT_WS_PORT_BASE + (scenario_index * 100)))
  local info_start=$((MQTT_INFO_PORT_BASE + (scenario_index * 100)))

  MQTT_BROKER_PORT="$(find_free_port "$broker_start")"
  MQTT_WS_PORT="$(find_free_port "$ws_start")"
  MQTT_INFO_PORT="$(find_free_port "$info_start")"
  if [[ -z "$MQTT_BROKER_PORT" || -z "$MQTT_WS_PORT" || -z "$MQTT_INFO_PORT" ]]; then
    echo "Failed to allocate free ports for profile index $scenario_index" >&2
    return 1
  fi

  MQTT_BROKER_ADDR=":${MQTT_BROKER_PORT}"
  MQTT_WS_ADDR=":${MQTT_WS_PORT}"
  MQTT_INFO_ADDR=":${MQTT_INFO_PORT}"
  MQTT_BROKER_URL="ssl://${MQTT_BROKER_HOST}:${MQTT_BROKER_PORT}"
  MQTT_INFO_URL="http://127.0.0.1:${MQTT_INFO_PORT}/"
}

effective_modules() {
  local modules="$TLS_PROFILE_MODULES"
  if [[ -n "$TLS_PROFILE_EXTRA_MODULES" ]]; then
    modules="$modules,$TLS_PROFILE_EXTRA_MODULES"
  fi
  echo "$modules"
}

ensure_tls_certs() {
  if [[ -f "$MQTT_TLS_CERT_FILE" && -f "$MQTT_TLS_KEY_FILE" && -f "$MQTT_TLS_CA_FILE" ]]; then
    return
  fi
  mkdir -p "$CERT_DIR"
  "$GENERATE_CERTS_SCRIPT" "$CERT_DIR"
}

build_load_client_binary() {
  (
    cd "$ROOT_DIR"
    go build -o "$LOAD_CLIENT_BIN" ./client/load
  )
}

build_broker_binary() {
  (
    cd "$BROKER_DIR"
    go build -o "$BROKER_BIN" ./cmd/main.go
  )
}

ensure_plot_dependencies() {
  if ! "$PYTHON_BIN" -c 'import matplotlib' >/dev/null 2>&1; then
    echo "matplotlib is required for plot generation. Install it with: $PYTHON_BIN -m pip install matplotlib" >&2
    return 1
  fi
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
  local profile="$1"
  local modules="$2"
  local broker_log="$3"

  "$BROKER_BIN" \
    --tcp "$MQTT_BROKER_ADDR" \
    --ws "$MQTT_WS_ADDR" \
    --info "$MQTT_INFO_ADDR" \
    --tls-cert-file "$MQTT_TLS_CERT_FILE" \
    --tls-key-file "$MQTT_TLS_KEY_FILE" \
    --modules "$modules" \
    --tls-profile "$profile" \
    >"$broker_log" 2>&1 &
  BROKER_PID="$!"

  for _ in $(seq 1 120); do
    if ! kill -0 "$BROKER_PID" 2>/dev/null; then
      echo "Broker process exited before ready state for profile $profile." >&2
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
  local cpu_key="$3"
  local rss_key="$4"
  local cpu rss last_cpu last_rss sleep_seconds proc_stat utime stime total_jiffies last_jiffies
  sleep_seconds="$(awk -v ms="$STATS_SAMPLE_INTERVAL_MS" 'BEGIN {printf "%.3f", ms/1000}')"
  last_cpu="0"
  last_rss="0"
  last_jiffies="0"
  echo "timestamp_iso,$cpu_key,$rss_key,cpu_jiffies" >"$out_csv"

  while kill -0 "$LOAD_PID" 2>/dev/null; do
    read -r cpu rss _ < <(ps -p "$target_pid" -o %cpu=,rss=,vsz= | awk 'NR==1 {print $1, $2, $3}')
    total_jiffies=""
    if [[ -r "/proc/$target_pid/stat" ]]; then
      proc_stat="$(cat "/proc/$target_pid/stat" 2>/dev/null || true)"
      if [[ -n "$proc_stat" ]]; then
        utime="$(awk '{print $14}' <<<"$proc_stat")"
        stime="$(awk '{print $15}' <<<"$proc_stat")"
        if [[ "$utime" =~ ^[0-9]+$ && "$stime" =~ ^[0-9]+$ ]]; then
          total_jiffies="$((utime + stime))"
        fi
      fi
    fi
    if [[ -n "${cpu:-}" ]]; then
      last_cpu="$cpu"
      last_rss="${rss:-0}"
      if [[ -n "${total_jiffies:-}" ]]; then
        last_jiffies="$total_jiffies"
      fi
      printf "%s,%s,%s,%s\n" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$last_cpu" "$last_rss" "$last_jiffies" >>"$out_csv"
    fi
    sleep "$sleep_seconds"
  done

  read -r cpu rss _ < <(ps -p "$target_pid" -o %cpu=,rss=,vsz= | awk 'NR==1 {print $1, $2, $3}')
  if [[ -n "${cpu:-}" ]]; then
    last_cpu="$cpu"
    last_rss="${rss:-0}"
  fi
  printf "%s,%s,%s,%s\n" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$last_cpu" "$last_rss" "$last_jiffies" >>"$out_csv"
}

probe_tls_negotiation() {
  local out_csv="$1"
  local out_log="$2"
  local protocol="na"
  local cipher="na"
  local key_exchange="na"
  local openssl_available="false"

  if command -v openssl >/dev/null 2>&1; then
    openssl_available="true"
    printf '' | openssl s_client \
      -connect "${MQTT_BROKER_HOST}:${MQTT_BROKER_PORT}" \
      -servername "$MQTT_TLS_SERVER_NAME" \
      -CAfile "$MQTT_TLS_CA_FILE" >"$out_log" 2>&1 || true

    protocol="$(awk -F: '/Protocol[[:space:]]*:/{gsub(/^[[:space:]]+/, "", $2); print $2; exit}' "$out_log")"
    cipher="$(awk '
      /Cipher is / {
        split($0, parts, "Cipher is ")
        if (parts[2] != "") {print parts[2]; exit}
      }
      /Cipher[[:space:]]*:/ {
        split($0, parts, ":")
        gsub(/^[[:space:]]+/, "", parts[2])
        if (parts[2] != "" && parts[2] != "0000") {print parts[2]; exit}
      }
    ' "$out_log")"
    key_exchange="$(awk -F: '/Peer Temp Key[[:space:]]*:/{gsub(/^[[:space:]]+/, "", $2); print $2; exit}' "$out_log")"
  else
    : >"$out_log"
  fi

  protocol="${protocol:-na}"
  cipher="${cipher:-na}"
  key_exchange="${key_exchange:-na}"
  {
    echo "probe_key,probe_value"
    echo "openssl_available,$openssl_available"
    echo "negotiated_protocol,$protocol"
    echo "negotiated_cipher,$cipher"
    echo "negotiated_key_exchange,$key_exchange"
  } >"$out_csv"

  echo "$protocol|$cipher|$key_exchange|$out_csv|$out_log"
}

run_profile_scenario() {
  local profile="$1"
  local modules="$2"
  local scenario_index="$3"
  local modules_csv="${modules//,/+}"
  local key_exchange_csv
  local scenario_dir="$RAW_DIR/${profile,,}"
  local broker_log="$scenario_dir/broker.log"
  local connect_csv="$scenario_dir/connect_latency.csv"
  local connect_log="$scenario_dir/connect_latency.log"
  local load_log="$scenario_dir/load.log"
  local broker_stats_csv="$scenario_dir/broker_stats.csv"
  local client_stats_csv="$scenario_dir/client_stats.csv"
  local tls_probe_csv="$scenario_dir/tls_probe.csv"
  local tls_probe_log="$scenario_dir/tls_probe.log"
  local tls_probe_result negotiated_protocol negotiated_cipher negotiated_key_exchange
  local connect_line load_line
  local handshake_avg handshake_p95 total_publishes connect_errors publish_errors load_duration throughput
  local broker_cpu_avg broker_cpu_peak broker_mem_avg_mib broker_mem_peak_mib
  local client_cpu_avg client_cpu_peak client_mem_avg_mib client_mem_peak_mib
  local client_first_jiffies client_last_jiffies client_delta_jiffies client_cpu_from_jiffies
  local broker_first_jiffies broker_last_jiffies broker_delta_jiffies broker_cpu_from_jiffies
  local load_exit_code="0"

  mkdir -p "$scenario_dir"
  configure_ports "$scenario_index"
  start_broker "$profile" "$modules" "$broker_log"
  tls_probe_result="$(probe_tls_negotiation "$tls_probe_csv" "$tls_probe_log")"
  IFS='|' read -r negotiated_protocol negotiated_cipher negotiated_key_exchange _ _ <<<"$tls_probe_result"
  key_exchange_csv="${negotiated_key_exchange//,/;}"
  case "$profile" in
    LOW_POWER)
      if [[ "$negotiated_protocol" != "TLSv1.2" ]]; then
        echo "LOW_POWER profile negotiated unexpected protocol: $negotiated_protocol (expected TLSv1.2)" >&2
        return 1
      fi
      if [[ "$negotiated_cipher" != *"CHACHA20"* ]]; then
        echo "LOW_POWER profile negotiated unexpected cipher: $negotiated_cipher (expected CHACHA20)" >&2
        return 1
      fi
      ;;
    HIGH_SECURITY)
      if [[ "$negotiated_protocol" != "TLSv1.3" ]]; then
        echo "HIGH_SECURITY profile negotiated unexpected protocol: $negotiated_protocol (expected TLSv1.3)" >&2
        return 1
      fi
      if [[ "$negotiated_cipher" != "TLS_AES_256_GCM_SHA384" && "$negotiated_cipher" != "TLS_AES_128_GCM_SHA256" ]]; then
        echo "HIGH_SECURITY profile negotiated unexpected cipher: $negotiated_cipher (expected TLS_AES_256_GCM_SHA384 or TLS_AES_128_GCM_SHA256)" >&2
        return 1
      fi
      if [[ "$negotiated_key_exchange" != *"secp384r1"* && "$negotiated_key_exchange" != *"secp521r1"* ]]; then
        echo "HIGH_SECURITY profile negotiated unexpected key exchange: $negotiated_key_exchange (expected secp384r1 or secp521r1)" >&2
        return 1
      fi
      ;;
  esac

  # Settle delay: let broker CPU baseline stabilize before measurement
  if [[ "$SETTLE_DELAY_S" -gt 0 ]]; then
    sleep "$SETTLE_DELAY_S"
  fi

  (
    cd "$ROOT_DIR"
    MQTT_TLS_CA_FILE="$MQTT_TLS_CA_FILE" \
      MQTT_TLS_SERVER_NAME="$MQTT_TLS_SERVER_NAME" \
      MQTT_TLS_INSECURE_SKIP_VERIFY="$MQTT_TLS_INSECURE_SKIP_VERIFY" \
      MQTT_TLS_SESSION_CACHE_SIZE="$CONNECT_TLS_SESSION_CACHE_SIZE" \
      go run ./client/probe connect \
        --broker "$MQTT_BROKER_URL" \
        --attempts "$CONNECT_ATTEMPTS" \
        --concurrency "$CONNECT_CONCURRENCY" \
        --timeout-ms "$CONNECT_TIMEOUT_MS" \
        --out "$connect_csv"
  ) >"$connect_log" 2>&1

  MQTT_BROKER_URL="$MQTT_BROKER_URL" \
    MQTT_TLS_CA_FILE="$MQTT_TLS_CA_FILE" \
    MQTT_TLS_SERVER_NAME="$MQTT_TLS_SERVER_NAME" \
    MQTT_TLS_INSECURE_SKIP_VERIFY="$MQTT_TLS_INSECURE_SKIP_VERIFY" \
    MQTT_TLS_SESSION_CACHE_SIZE="$LOAD_TLS_SESSION_CACHE_SIZE" \
    "$LOAD_CLIENT_BIN" "$LOAD_WORKERS" "$LOAD_MESSAGES_PER_WORKER" "$LOAD_DELAY_MS" "$LOAD_RECONNECT_EVERY" >"$load_log" 2>&1 &
  LOAD_PID="$!"

  monitor_process_stats "$BROKER_PID" "$broker_stats_csv" "broker_cpu_pct" "broker_rss_kb" &
  BROKER_MONITOR_PID="$!"
  monitor_process_stats "$LOAD_PID" "$client_stats_csv" "client_cpu_pct" "client_rss_kb" &
  CLIENT_MONITOR_PID="$!"

  if ! wait "$LOAD_PID"; then
    load_exit_code="$?"
  fi
  LOAD_PID=""
  wait "$BROKER_MONITOR_PID" || true
  wait "$CLIENT_MONITOR_PID" || true
  BROKER_MONITOR_PID=""
  CLIENT_MONITOR_PID=""

  if [[ "$load_exit_code" -ne 0 ]]; then
    echo "Load benchmark failed for profile $profile. Check $load_log" >&2
    return 1
  fi

  connect_line="$(grep 'SUMMARY mode=connect' "$connect_log" | tail -n 1 || true)"
  load_line="$(grep 'SUMMARY workers=' "$load_log" | tail -n 1 || true)"

  handshake_avg="$(extract_summary_value "$connect_line" "avg_ms")"
  handshake_p95="$(extract_summary_value "$connect_line" "p95_ms")"
  total_publishes="$(extract_summary_value "$load_line" "total_publishes")"
  connect_errors="$(extract_summary_value "$load_line" "connect_errors")"
  publish_errors="$(extract_summary_value "$load_line" "publish_errors")"
  load_duration="$(extract_summary_value "$load_line" "duration_seconds")"

  handshake_avg="${handshake_avg:-0}"
  handshake_p95="${handshake_p95:-0}"
  total_publishes="${total_publishes:-0}"
  connect_errors="${connect_errors:-0}"
  publish_errors="${publish_errors:-0}"
  load_duration="${load_duration:-0}"
  throughput="$(awk -v p="$total_publishes" -v d="$load_duration" 'BEGIN {if (d > 0) printf "%.3f", p / d; else print 0}')"

  # Compute ps-based CPU averages, skipping initial warmup outlier samples
  broker_cpu_avg="$(awk -F, -v skip="$STATS_WARMUP_SKIP" 'NR > 1 + skip {s += ($2 + 0); n++} END {if (n > 0) printf "%.3f", s / n; else print 0}' "$broker_stats_csv")"
  broker_cpu_peak="$(awk -F, 'NR > 1 && ($2 + 0) > m {m = $2 + 0} END {printf "%.3f", m + 0}' "$broker_stats_csv")"
  broker_mem_avg_mib="$(awk -F, 'NR > 1 {s += ($3 + 0); n++} END {if (n > 0) printf "%.3f", (s / n) / 1024; else print 0}' "$broker_stats_csv")"
  broker_mem_peak_mib="$(awk -F, 'NR > 1 && ($3 + 0) > m {m = $3 + 0} END {printf "%.3f", (m + 0) / 1024}' "$broker_stats_csv")"
  client_cpu_avg="$(awk -F, -v skip="$STATS_WARMUP_SKIP" 'NR > 1 + skip {s += ($2 + 0); n++} END {if (n > 0) printf "%.3f", s / n; else print 0}' "$client_stats_csv")"
  client_cpu_peak="$(awk -F, -v skip="$STATS_WARMUP_SKIP" 'NR > 1 + skip && ($2 + 0) > m {m = $2 + 0} END {printf "%.3f", m + 0}' "$client_stats_csv")"
  client_mem_avg_mib="$(awk -F, 'NR > 1 {s += ($3 + 0); n++} END {if (n > 0) printf "%.3f", (s / n) / 1024; else print 0}' "$client_stats_csv")"
  client_mem_peak_mib="$(awk -F, 'NR > 1 && ($3 + 0) > m {m = $3 + 0} END {printf "%.3f", (m + 0) / 1024}' "$client_stats_csv")"

  # Jiffies-based CPU: deterministic, accurate regardless of run duration
  # Broker jiffies
  broker_first_jiffies="$(awk -F, 'NR==2 && $4 ~ /^[0-9]+$/ {print $4}' "$broker_stats_csv")"
  broker_last_jiffies="$(awk -F, 'END {if ($4 ~ /^[0-9]+$/) print $4}' "$broker_stats_csv")"
  if [[ -n "$broker_first_jiffies" && -n "$broker_last_jiffies" ]]; then
    broker_delta_jiffies=$((broker_last_jiffies - broker_first_jiffies))
    if (( broker_delta_jiffies > 0 )); then
      broker_cpu_from_jiffies="$(awk -v dj="$broker_delta_jiffies" -v hz="$CLK_TCK" -v d="$load_duration" 'BEGIN {if (d > 0 && hz > 0) printf "%.3f", ((dj / hz) / d) * 100; else print 0}')"
      if [[ -n "$broker_cpu_from_jiffies" ]]; then
        broker_cpu_avg="$broker_cpu_from_jiffies"
      fi
    fi
  fi

  # Client jiffies
  client_first_jiffies="$(awk -F, 'NR==2 && $4 ~ /^[0-9]+$/ {print $4}' "$client_stats_csv")"
  client_last_jiffies="$(awk -F, 'END {if ($4 ~ /^[0-9]+$/) print $4}' "$client_stats_csv")"
  if [[ -n "$client_first_jiffies" && -n "$client_last_jiffies" ]]; then
    client_delta_jiffies=$((client_last_jiffies - client_first_jiffies))
    if (( client_delta_jiffies > 0 )); then
      client_cpu_from_jiffies="$(awk -v dj="$client_delta_jiffies" -v hz="$CLK_TCK" -v d="$load_duration" 'BEGIN {if (d > 0 && hz > 0) printf "%.3f", ((dj / hz) / d) * 100; else print 0}')"
      if [[ -n "$client_cpu_from_jiffies" ]]; then
        client_cpu_avg="$client_cpu_from_jiffies"
      fi
    fi
  fi

  printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" \
    "$profile" \
    "$modules_csv" \
    "$negotiated_protocol" \
    "$negotiated_cipher" \
    "$key_exchange_csv" \
    "$handshake_avg" \
    "$handshake_p95" \
    "$broker_cpu_avg" \
    "$broker_cpu_peak" \
    "$broker_mem_avg_mib" \
    "$broker_mem_peak_mib" \
    "$client_cpu_avg" \
    "$client_cpu_peak" \
    "$client_mem_avg_mib" \
    "$client_mem_peak_mib" \
    "$throughput" \
    "$total_publishes" \
    "$connect_errors" \
    "$publish_errors" \
    "$load_duration" \
    "$connect_csv" \
    "$connect_log" \
    "$load_log" \
    "$broker_log" \
    "$broker_stats_csv" \
    "$client_stats_csv" \
    "$tls_probe_csv" \
    "$tls_probe_log" >>"$SUMMARY_CSV"

  stop_broker
}

build_manifest() {
  {
    echo "path,type"
    echo "summary.csv,summary"
    echo "run_info.txt,metadata"
    echo "plot_generation.log,metadata"
    echo "metadata/host_info.csv,metadata"
    echo "metadata/git_info.txt,metadata"
    echo "metadata/environment.txt,metadata"
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

RUN_STARTED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ensure_tls_certs
ensure_plot_dependencies
build_load_client_binary
build_broker_binary
capture_metadata

modules="$(effective_modules)"
IFS=',' read -r -a profile_array <<<"$TLS_PROFILES"
if [[ "${#profile_array[@]}" -eq 0 ]]; then
  echo "No TLS profiles configured. Set TLS_PROFILES." >&2
  exit 1
fi

scenario_index=0
for profile in "${profile_array[@]}"; do
  profile="$(echo "$profile" | xargs)"
  if [[ -z "$profile" ]]; then
    continue
  fi
  echo "Running TLS profile scenario: profile=$profile modules=$modules"
  run_profile_scenario "$profile" "$modules" "$scenario_index"
  scenario_index=$((scenario_index + 1))
done

if [[ ! -f "$PLOT_SCRIPT" ]]; then
  echo "Plot script not found: $PLOT_SCRIPT" >&2
  exit 1
fi

"$PYTHON_BIN" "$PLOT_SCRIPT" \
  --summary-csv "$SUMMARY_CSV" \
  --output-dir "$PLOTS_DIR" >"$PLOT_LOG" 2>&1

for required_file in \
  "$PLOTS_DIR/handshake_latency.png" \
  "$PLOTS_DIR/throughput_msgs_per_s.png" \
  "$PLOTS_DIR/broker_cpu_usage.png" \
  "$PLOTS_DIR/broker_memory_usage.png" \
  "$PLOTS_DIR/client_cpu_usage.png" \
  "$PLOTS_DIR/client_memory_usage.png" \
  "$PLOTS_DIR/plots_manifest.csv"; do
  if [[ ! -f "$required_file" ]]; then
    echo "Missing expected plot artifact: $required_file" >&2
    echo "Plot log: $PLOT_LOG" >&2
    exit 1
  fi
done

build_manifest
RUN_FINISHED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cat >"$RUN_INFO_TXT" <<EOF
run_started_utc=$RUN_STARTED_UTC
run_finished_utc=$RUN_FINISHED_UTC
results_root=$RESULTS_ROOT
results_dir=$RESULTS_DIR
summary_csv=$SUMMARY_CSV
manifest_csv=$MANIFEST_CSV
plots_dir=$PLOTS_DIR
plot_generation_log=$PLOT_LOG
load_client_binary=$LOAD_CLIENT_BIN
tls_profiles=$TLS_PROFILES
modules=$modules
mqtt_ws_port_base=$MQTT_WS_PORT_BASE
mqtt_tls_cert_file=$MQTT_TLS_CERT_FILE
mqtt_tls_key_file=$MQTT_TLS_KEY_FILE
mqtt_tls_ca_file=$MQTT_TLS_CA_FILE
mqtt_tls_server_name=$MQTT_TLS_SERVER_NAME
connect_attempts=$CONNECT_ATTEMPTS
connect_concurrency=$CONNECT_CONCURRENCY
connect_timeout_ms=$CONNECT_TIMEOUT_MS
connect_tls_session_cache_size=$CONNECT_TLS_SESSION_CACHE_SIZE
load_workers=$LOAD_WORKERS
load_messages_per_worker=$LOAD_MESSAGES_PER_WORKER
load_delay_ms=$LOAD_DELAY_MS
load_tls_session_cache_size=$LOAD_TLS_SESSION_CACHE_SIZE
load_reconnect_every=$LOAD_RECONNECT_EVERY
stats_sample_interval_ms=$STATS_SAMPLE_INTERVAL_MS
settle_delay_s=$SETTLE_DELAY_S
stats_warmup_skip=$STATS_WARMUP_SKIP
EOF

echo "TLS profile comparison complete."
echo "Results directory: $RESULTS_DIR"
echo "Summary CSV: $SUMMARY_CSV"
