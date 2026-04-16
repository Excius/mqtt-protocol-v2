#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RESULTS_ARG="${1:-}"
if [[ -z "$RESULTS_ARG" ]]; then
  RESULTS_DIR="$ROOT_DIR/results/present_state_$(date +%Y%m%d_%H%M%S)"
elif [[ "$RESULTS_ARG" = /* ]]; then
  RESULTS_DIR="$RESULTS_ARG"
else
  RESULTS_DIR="$ROOT_DIR/$RESULTS_ARG"
fi
LOAD_DIR="$RESULTS_DIR/load"
LOAD_RAW_DIR="$LOAD_DIR/raw"
LOAD_PLOTS_DIR="$LOAD_DIR/plots"
LATENCY_DIR="$RESULTS_DIR/latency"
LATENCY_RAW_DIR="$LATENCY_DIR/raw"
LATENCY_PLOTS_DIR="$LATENCY_DIR/plots"
METADATA_DIR="$RESULTS_DIR/metadata"

PYTHON_BIN="${PYTHON_BIN:-python3}"
BROKER_DIR="$ROOT_DIR/broker"
PLOT_SCRIPT="$ROOT_DIR/experiments/baseline/plot_present_state.py"
MQTT_BROKER_ADDR="${MQTT_BROKER_ADDR:-:1883}"
MQTT_INFO_ADDR="${MQTT_INFO_ADDR:-:8080}"
MQTT_INFO_URL="${MQTT_INFO_URL:-http://127.0.0.1:8080/}"
MQTT_BROKER_URL="${MQTT_BROKER_URL:-tcp://127.0.0.1:1883}"
MQTT_TLS_CERT_FILE="${MQTT_TLS_CERT_FILE:-}"
MQTT_TLS_KEY_FILE="${MQTT_TLS_KEY_FILE:-}"
MQTT_TLS_CA_FILE="${MQTT_TLS_CA_FILE:-}"
MQTT_TLS_SERVER_NAME="${MQTT_TLS_SERVER_NAME:-}"
MQTT_TLS_INSECURE_SKIP_VERIFY="${MQTT_TLS_INSECURE_SKIP_VERIFY:-false}"
MQTT_TLS_SESSION_RESUMPTION="${MQTT_TLS_SESSION_RESUMPTION:-true}"
MQTT_TLS_SESSION_CACHE_SIZE="${MQTT_TLS_SESSION_CACHE_SIZE:-100}"
MQTT_BROKER_MODULES="${MQTT_BROKER_MODULES:-}"
MQTT_TCP_PORT="${MQTT_BROKER_ADDR##*:}"

export MQTT_BROKER_URL
export MQTT_TLS_CA_FILE
export MQTT_TLS_SERVER_NAME
export MQTT_TLS_INSECURE_SKIP_VERIFY
export MQTT_TLS_SESSION_CACHE_SIZE

BROKER_PID=""
BROKER_MODULES_EFFECTIVE=""
LOAD_PID=""
MONITOR_PID=""

mkdir -p "$RESULTS_DIR" "$LOAD_DIR" "$LOAD_RAW_DIR" "$LOAD_PLOTS_DIR" "$LATENCY_RAW_DIR" "$LATENCY_PLOTS_DIR" "$METADATA_DIR"

capture_host_info() {
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
    echo "swap_total_kb,$(awk '/SwapTotal/ {print $2}' /proc/meminfo)"
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
    echo "RUN_STARTED_UTC=$RUN_STARTED_UTC"
    echo "RESULTS_DIR=$RESULTS_DIR"
    echo "PYTHON_BIN=$PYTHON_BIN"
    env | sort
  } >"$env_info"
}

cleanup() {
  if [[ -n "$MONITOR_PID" ]] && kill -0 "$MONITOR_PID" 2>/dev/null; then
    kill "$MONITOR_PID" 2>/dev/null || true
    wait "$MONITOR_PID" 2>/dev/null || true
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

resolve_broker_modules() {
  if [[ -n "$MQTT_BROKER_MODULES" ]]; then
    echo "$MQTT_BROKER_MODULES"
    return 0
  fi

  local modules=""
  if [[ "${MQTT_TLS_SESSION_RESUMPTION,,}" == "true" ]]; then
    modules="tls-session-resumption"
  fi
  if [[ -z "$modules" ]]; then
    modules="baseline"
  fi
  echo "$modules"
}

start_broker() {
  local broker_log="$LATENCY_DIR/broker.log"
  local modules
  modules="$(resolve_broker_modules)"
  BROKER_MODULES_EFFECTIVE="$modules"

  if [[ -n "$MQTT_TLS_CERT_FILE" || -n "$MQTT_TLS_KEY_FILE" ]]; then
    if [[ -z "$MQTT_TLS_CERT_FILE" || -z "$MQTT_TLS_KEY_FILE" ]]; then
      echo "Both MQTT_TLS_CERT_FILE and MQTT_TLS_KEY_FILE must be set for TLS broker mode." >&2
      return 1
    fi
  fi

  local broker_cmd=(go run ./cmd/main.go --tcp "$MQTT_BROKER_ADDR" --info "$MQTT_INFO_ADDR" --modules "$modules")
  if [[ -n "$MQTT_TLS_CERT_FILE" && -n "$MQTT_TLS_KEY_FILE" ]]; then
    broker_cmd+=(
      --tls-cert-file "$MQTT_TLS_CERT_FILE"
      --tls-key-file "$MQTT_TLS_KEY_FILE"
    )
  fi

  (
    cd "$BROKER_DIR"
    exec "${broker_cmd[@]}" >"$broker_log" 2>&1
  ) &
  BROKER_PID="$!"

  for _ in $(seq 1 80); do
    if curl -sf --max-time 0.2 "$MQTT_INFO_URL" >/dev/null 2>&1; then
      if command -v ss >/dev/null 2>&1; then
        local listener_pid
        listener_pid="$(ss -ltnp "sport = :$MQTT_TCP_PORT" 2>/dev/null | awk -F'pid=' 'NR > 1 && NF > 1 {split($2, a, ","); print a[1]; exit}')"
        if [[ -n "$listener_pid" ]]; then
          BROKER_PID="$listener_pid"
        fi
      fi
      return 0
    fi
    sleep 0.25
  done

  echo "Broker did not become ready on $MQTT_INFO_URL" >&2
  return 1
}

read_cpu_parts() {
  awk '/^cpu / {print $2, $3, $4, $5, $6, $7, $8, $9}' /proc/stat
}

read_net_totals() {
  awk 'NR > 2 {gsub(":", "", $1); rx += $2; tx += $10} END {printf "%d %d\n", rx, tx}' /proc/net/dev
}

capture_idle_baseline() {
  local out_csv="$LATENCY_RAW_DIR/idle_30s.csv"
  local duration_s="${IDLE_DURATION_S:-30}"

  cat >"$out_csv" <<'EOF'
timestamp_iso,epoch_ms,elapsed_s,broker_cpu_pct,broker_rss_kb,sys_cpu_user_pct,sys_cpu_system_pct,sys_cpu_idle_pct,net_rx_bytes_delta,net_tx_bytes_delta,sys_clients_connected,sys_messages_received,sys_memory_alloc,sys_threads,sys_packets_received,sys_packets_sent,sys_subscriptions,sys_inflight,sys_inflight_dropped,sys_messages_dropped,sys_retained,sys_clients_total,sys_clients_maximum
EOF

  local start_ms
  start_ms="$(date +%s%3N)"

  read -r prev_user prev_nice prev_sys prev_idle prev_iowait prev_irq prev_softirq prev_steal < <(read_cpu_parts)
  read -r prev_net_rx prev_net_tx < <(read_net_totals)

  for _ in $(seq 1 "$duration_s"); do
    local now_ms elapsed_s broker_cpu broker_rss
    local cur_user cur_nice cur_sys cur_idle cur_iowait cur_irq cur_softirq cur_steal
    local userd niced sysd idled iowaitd irqd softirqd steald totald user_pct sys_pct idle_pct
    local net_rx net_tx delta_rx delta_tx
    local sys_json sys_clients sys_messages sys_mem sys_threads
    local sys_packets_rx sys_packets_tx sys_subscriptions sys_inflight sys_inflight_dropped
    local sys_messages_dropped sys_retained sys_clients_total sys_clients_max

    now_ms="$(date +%s%3N)"
    elapsed_s="$(awk -v s="$start_ms" -v n="$now_ms" 'BEGIN {printf "%.3f", (n - s) / 1000}')"

    read -r broker_cpu broker_rss _ < <(ps -p "$BROKER_PID" -o %cpu=,rss=,vsz= | awk 'NR==1 {print $1, $2, $3}')

    read -r cur_user cur_nice cur_sys cur_idle cur_iowait cur_irq cur_softirq cur_steal < <(read_cpu_parts)
    userd=$((cur_user - prev_user))
    niced=$((cur_nice - prev_nice))
    sysd=$((cur_sys - prev_sys))
    idled=$((cur_idle - prev_idle))
    iowaitd=$((cur_iowait - prev_iowait))
    irqd=$((cur_irq - prev_irq))
    softirqd=$((cur_softirq - prev_softirq))
    steald=$((cur_steal - prev_steal))
    totald=$((userd + niced + sysd + idled + iowaitd + irqd + softirqd + steald))

    if [[ "$totald" -gt 0 ]]; then
      user_pct="$(awk -v v="$((userd + niced))" -v t="$totald" 'BEGIN {printf "%.2f", (v * 100) / t}')"
      sys_pct="$(awk -v v="$((sysd + irqd + softirqd))" -v t="$totald" 'BEGIN {printf "%.2f", (v * 100) / t}')"
      idle_pct="$(awk -v v="$((idled + iowaitd))" -v t="$totald" 'BEGIN {printf "%.2f", (v * 100) / t}')"
    else
      user_pct="0"
      sys_pct="0"
      idle_pct="0"
    fi

    prev_user="$cur_user"
    prev_nice="$cur_nice"
    prev_sys="$cur_sys"
    prev_idle="$cur_idle"
    prev_iowait="$cur_iowait"
    prev_irq="$cur_irq"
    prev_softirq="$cur_softirq"
    prev_steal="$cur_steal"

    read -r net_rx net_tx < <(read_net_totals)
    delta_rx=$((net_rx - prev_net_rx))
    delta_tx=$((net_tx - prev_net_tx))
    prev_net_rx="$net_rx"
    prev_net_tx="$net_tx"

    sys_json="$(curl -sf --max-time 0.3 "$MQTT_INFO_URL" 2>/dev/null || true)"
    if [[ -n "$sys_json" ]]; then
      read -r sys_clients sys_messages _ _ sys_mem sys_threads < <(
        jq -r '[.clients_connected,.messages_received,.bytes_received,.bytes_sent,.memory_alloc,.threads] | @tsv' <<<"$sys_json"
      )
      read -r sys_packets_rx sys_packets_tx sys_subscriptions sys_inflight sys_inflight_dropped sys_messages_dropped sys_retained sys_clients_total sys_clients_max < <(
        jq -r '[.packets_received,.packets_sent,.subscriptions,.inflight,.inflight_dropped,.messages_dropped,.retained,.clients_total,.clients_maximum] | @tsv' <<<"$sys_json"
      )
    else
      sys_clients=""
      sys_messages=""
      sys_mem=""
      sys_threads=""
      sys_packets_rx=""
      sys_packets_tx=""
      sys_subscriptions=""
      sys_inflight=""
      sys_inflight_dropped=""
      sys_messages_dropped=""
      sys_retained=""
      sys_clients_total=""
      sys_clients_max=""
    fi

    printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" \
      "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "$now_ms" \
      "$elapsed_s" \
      "$broker_cpu" \
      "$broker_rss" \
      "$user_pct" \
      "$sys_pct" \
      "$idle_pct" \
      "$delta_rx" \
      "$delta_tx" \
      "$sys_clients" \
      "$sys_messages" \
      "$sys_mem" \
      "$sys_threads" \
      "$sys_packets_rx" \
      "$sys_packets_tx" \
      "$sys_subscriptions" \
      "$sys_inflight" \
      "$sys_inflight_dropped" \
      "$sys_messages_dropped" \
      "$sys_retained" \
      "$sys_clients_total" \
      "$sys_clients_max" >>"$out_csv"

    sleep 1
  done
}

extract_summary_value() {
  local line="$1"
  local key="$2"
  awk -v k="$key" '{for (i = 1; i <= NF; i++) {split($i, a, "="); if (a[1] == k) {print a[2]; break}}}' <<<"$line"
}

capture_load_sample() {
  local tier="$1"
  local workers="$2"
  local messages="$3"
  local delay_ms="$4"
  local csv_file="$5"
  local start_ms="$6"

  local now_ms elapsed_s broker_cpu broker_rss broker_vsz loadavg
  local cur_user cur_nice cur_sys cur_idle cur_iowait cur_irq cur_softirq cur_steal
  local userd niced sysd idled iowaitd irqd softirqd steald totald user_pct sys_pct idle_pct
  local net_rx net_tx delta_rx delta_tx
  local sys_json sys_clients sys_messages sys_bytes_rx sys_bytes_tx sys_mem_alloc sys_threads
  local sys_packets_rx sys_packets_tx sys_subscriptions sys_inflight sys_inflight_dropped
  local sys_messages_dropped sys_retained sys_clients_total sys_clients_max

  now_ms="$(date +%s%3N)"
  elapsed_s="$(awk -v s="$start_ms" -v n="$now_ms" 'BEGIN {printf "%.3f", (n - s) / 1000}')"

  if kill -0 "$BROKER_PID" 2>/dev/null; then
    read -r broker_cpu broker_rss broker_vsz < <(ps -p "$BROKER_PID" -o %cpu=,rss=,vsz= | awk 'NR==1 {print $1, $2, $3}')
  else
    broker_cpu="0"
    broker_rss="0"
    broker_vsz="0"
  fi

  read -r cur_user cur_nice cur_sys cur_idle cur_iowait cur_irq cur_softirq cur_steal < <(read_cpu_parts)

  userd=$((cur_user - PREV_USER))
  niced=$((cur_nice - PREV_NICE))
  sysd=$((cur_sys - PREV_SYS))
  idled=$((cur_idle - PREV_IDLE))
  iowaitd=$((cur_iowait - PREV_IOWAIT))
  irqd=$((cur_irq - PREV_IRQ))
  softirqd=$((cur_softirq - PREV_SOFTIRQ))
  steald=$((cur_steal - PREV_STEAL))
  totald=$((userd + niced + sysd + idled + iowaitd + irqd + softirqd + steald))

  if [[ "$totald" -gt 0 ]]; then
    user_pct="$(awk -v v="$((userd + niced))" -v t="$totald" 'BEGIN {printf "%.2f", (v * 100) / t}')"
    sys_pct="$(awk -v v="$((sysd + irqd + softirqd))" -v t="$totald" 'BEGIN {printf "%.2f", (v * 100) / t}')"
    idle_pct="$(awk -v v="$((idled + iowaitd))" -v t="$totald" 'BEGIN {printf "%.2f", (v * 100) / t}')"
  else
    user_pct="0"
    sys_pct="0"
    idle_pct="0"
  fi

  PREV_USER="$cur_user"
  PREV_NICE="$cur_nice"
  PREV_SYS="$cur_sys"
  PREV_IDLE="$cur_idle"
  PREV_IOWAIT="$cur_iowait"
  PREV_IRQ="$cur_irq"
  PREV_SOFTIRQ="$cur_softirq"
  PREV_STEAL="$cur_steal"

  loadavg="$(awk '{print $1}' /proc/loadavg)"

  read -r net_rx net_tx < <(read_net_totals)
  delta_rx=$((net_rx - PREV_NET_RX))
  delta_tx=$((net_tx - PREV_NET_TX))
  PREV_NET_RX="$net_rx"
  PREV_NET_TX="$net_tx"

  sys_json="$(curl -sf --max-time 0.3 "$MQTT_INFO_URL" 2>/dev/null || true)"
  if [[ -n "$sys_json" ]]; then
    read -r sys_clients sys_messages sys_bytes_rx sys_bytes_tx sys_mem_alloc sys_threads < <(
      jq -r '[.clients_connected,.messages_received,.bytes_received,.bytes_sent,.memory_alloc,.threads] | @tsv' <<<"$sys_json"
    )
    read -r sys_packets_rx sys_packets_tx sys_subscriptions sys_inflight sys_inflight_dropped sys_messages_dropped sys_retained sys_clients_total sys_clients_max < <(
      jq -r '[.packets_received,.packets_sent,.subscriptions,.inflight,.inflight_dropped,.messages_dropped,.retained,.clients_total,.clients_maximum] | @tsv' <<<"$sys_json"
    )
  else
    sys_clients=""
    sys_messages=""
    sys_bytes_rx=""
    sys_bytes_tx=""
    sys_mem_alloc=""
    sys_threads=""
    sys_packets_rx=""
    sys_packets_tx=""
    sys_subscriptions=""
    sys_inflight=""
    sys_inflight_dropped=""
    sys_messages_dropped=""
    sys_retained=""
    sys_clients_total=""
    sys_clients_max=""
  fi

  printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    "$now_ms" \
    "$elapsed_s" \
    "$tier" \
    "$workers" \
    "$messages" \
    "$delay_ms" \
    "$broker_cpu" \
    "$broker_rss" \
    "$broker_vsz" \
    "$user_pct" \
    "$sys_pct" \
    "$idle_pct" \
    "$loadavg" \
    "$net_rx" \
    "$net_tx" \
    "$delta_rx" \
    "$delta_tx" \
    "$sys_clients" \
    "$sys_messages" \
    "$sys_bytes_rx" \
    "$sys_bytes_tx" \
    "$sys_mem_alloc" \
    "$sys_threads" \
    "$sys_packets_rx" \
    "$sys_packets_tx" \
    "$sys_subscriptions" \
    "$sys_inflight" \
    "$sys_inflight_dropped" \
    "$sys_messages_dropped" \
    "$sys_retained" \
    "$sys_clients_total" \
    "$sys_clients_max" >>"$csv_file"
}

monitor_load_tier() {
  local tier="$1"
  local workers="$2"
  local messages="$3"
  local delay_ms="$4"
  local csv_file="$5"

  cat >"$csv_file" <<'EOF'
timestamp_iso,epoch_ms,elapsed_s,tier,workers,messages_per_worker,delay_ms,broker_cpu_pct,broker_rss_kb,broker_vsz_kb,sys_cpu_user_pct,sys_cpu_system_pct,sys_cpu_idle_pct,loadavg1,net_rx_bytes_total,net_tx_bytes_total,net_rx_bytes_delta,net_tx_bytes_delta,sys_clients_connected,sys_messages_received,sys_bytes_received,sys_bytes_sent,sys_memory_alloc,sys_threads,sys_packets_received,sys_packets_sent,sys_subscriptions,sys_inflight,sys_inflight_dropped,sys_messages_dropped,sys_retained,sys_clients_total,sys_clients_maximum
EOF

  local start_ms
  start_ms="$(date +%s%3N)"

  read -r PREV_USER PREV_NICE PREV_SYS PREV_IDLE PREV_IOWAIT PREV_IRQ PREV_SOFTIRQ PREV_STEAL < <(read_cpu_parts)
  read -r PREV_NET_RX PREV_NET_TX < <(read_net_totals)

  while kill -0 "$LOAD_PID" 2>/dev/null; do
    capture_load_sample "$tier" "$workers" "$messages" "$delay_ms" "$csv_file" "$start_ms"
    sleep 1
  done

  capture_load_sample "$tier" "$workers" "$messages" "$delay_ms" "$csv_file" "$start_ms"
}

append_load_tier_summary() {
  local summary_csv="$1"
  local tier="$2"
  local workers="$3"
  local messages="$4"
  local delay_ms="$5"
  local load_exit="$6"
  local csv_file="$7"
  local load_log="$8"

  local summary_line total_publishes connect_errors publish_errors duration_s
  local samples peak_cpu peak_rss avg_rss avg_rx avg_tx peak_clients peak_messages

  summary_line="$(grep 'SUMMARY ' "$load_log" | tail -n 1 || true)"
  total_publishes="$(extract_summary_value "$summary_line" "total_publishes")"
  connect_errors="$(extract_summary_value "$summary_line" "connect_errors")"
  publish_errors="$(extract_summary_value "$summary_line" "publish_errors")"
  duration_s="$(extract_summary_value "$summary_line" "duration_seconds")"

  total_publishes="${total_publishes:-0}"
  connect_errors="${connect_errors:-0}"
  publish_errors="${publish_errors:-0}"
  duration_s="${duration_s:-0}"

  samples="$(awk 'END {if (NR > 1) print NR - 1; else print 0}' "$csv_file")"
  peak_cpu="$(awk -F, 'NR > 1 && ($8 + 0) > m {m = $8 + 0} END {printf "%.2f", m + 0}' "$csv_file")"
  peak_rss="$(awk -F, 'NR > 1 && ($9 + 0) > m {m = $9 + 0} END {printf "%.0f", m + 0}' "$csv_file")"
  avg_rss="$(awk -F, 'NR > 1 {s += ($9 + 0); n++} END {if (n > 0) printf "%.2f", s / n; else printf "0"}' "$csv_file")"
  avg_rx="$(awk -F, 'NR > 1 {s += ($17 + 0); n++} END {if (n > 0) printf "%.2f", s / n; else printf "0"}' "$csv_file")"
  avg_tx="$(awk -F, 'NR > 1 {s += ($18 + 0); n++} END {if (n > 0) printf "%.2f", s / n; else printf "0"}' "$csv_file")"
  peak_clients="$(awk -F, 'NR > 1 && ($19 + 0) > m {m = $19 + 0} END {printf "%.0f", m + 0}' "$csv_file")"
  peak_messages="$(awk -F, 'NR > 1 && ($20 + 0) > m {m = $20 + 0} END {printf "%.0f", m + 0}' "$csv_file")"

  printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" \
    "$tier" \
    "$workers" \
    "$messages" \
    "$delay_ms" \
    "$load_exit" \
    "$total_publishes" \
    "$connect_errors" \
    "$publish_errors" \
    "$duration_s" \
    "$samples" \
    "$peak_cpu" \
    "$peak_rss" \
    "$avg_rss" \
    "$avg_rx" \
    "$avg_tx" \
    "$peak_clients" \
    "$peak_messages" \
    "$csv_file" \
    "$load_log" >>"$summary_csv"
}

run_load_tier() {
  local summary_csv="$1"
  local tier="$2"
  local workers="$3"
  local messages="$4"
  local delay_ms="$5"

  local csv_file="$LOAD_RAW_DIR/${tier}.csv"
  local load_log="$LOAD_RAW_DIR/${tier}_load.log"
  local load_exit="0"

  echo "Running tier=$tier workers=$workers messages_per_worker=$messages delay_ms=$delay_ms"

  (
    cd "$ROOT_DIR/client/load"
    go run . "$workers" "$messages" "$delay_ms" >"$load_log" 2>&1
  ) &
  LOAD_PID="$!"

  monitor_load_tier "$tier" "$workers" "$messages" "$delay_ms" "$csv_file" &
  MONITOR_PID="$!"

  if ! wait "$LOAD_PID"; then
    load_exit="$?"
  fi
  LOAD_PID=""

  wait "$MONITOR_PID" || true
  MONITOR_PID=""

  append_load_tier_summary "$summary_csv" "$tier" "$workers" "$messages" "$delay_ms" "$load_exit" "$csv_file" "$load_log"
}

derive_messages_per_worker() {
  local delay_ms="$1"
  local duration_s="$2"

  awk -v d="$delay_ms" -v s="$duration_s" 'BEGIN {
    if (d <= 0) {
      print 1
      exit
    }
    v = (s * 1000.0) / d
    n = int(v)
    if (v > n) {
      n++
    }
    if (n < 1) {
      n = 1
    }
    print n
  }'
}

run_load_capture() {
  local summary_csv="$LOAD_DIR/summary.csv"
  cat >"$summary_csv" <<'EOF'
tier,workers,messages_per_worker,delay_ms,load_exit_code,total_publishes,connect_errors,publish_errors,duration_seconds,sample_count,peak_broker_cpu_pct,peak_broker_rss_kb,avg_broker_rss_kb,avg_net_rx_bytes_per_sample,avg_net_tx_bytes_per_sample,peak_sys_clients_connected,peak_sys_messages_received,csv_file,load_log_file
EOF

  local use_equal_tier_duration="${USE_EQUAL_TIER_DURATION:-true}"
  local equal_tier_duration_s="${EQUAL_TIER_DURATION_S:-20}"

  local normal_workers="${NORMAL_WORKERS:-50}"
  local normal_delay_ms="${NORMAL_DELAY_MS:-20}"
  local high_workers="${HIGH_WORKERS:-200}"
  local high_delay_ms="${HIGH_DELAY_MS:-10}"
  local very_high_workers="${VERY_HIGH_WORKERS:-500}"
  local very_high_delay_ms="${VERY_HIGH_DELAY_MS:-5}"
  local normal_messages high_messages very_high_messages

  if [[ "$use_equal_tier_duration" == "true" ]]; then
    normal_messages="$(derive_messages_per_worker "$normal_delay_ms" "$equal_tier_duration_s")"
    high_messages="$(derive_messages_per_worker "$high_delay_ms" "$equal_tier_duration_s")"
    very_high_messages="$(derive_messages_per_worker "$very_high_delay_ms" "$equal_tier_duration_s")"
  else
    normal_messages="${NORMAL_MESSAGES:-1000}"
    high_messages="${HIGH_MESSAGES:-1200}"
    very_high_messages="${VERY_HIGH_MESSAGES:-1500}"
  fi

  run_load_tier "$summary_csv" "normal" "$normal_workers" "$normal_messages" "$normal_delay_ms"
  run_load_tier "$summary_csv" "high" "$high_workers" "$high_messages" "$high_delay_ms"
  run_load_tier "$summary_csv" "very_high" "$very_high_workers" "$very_high_messages" "$very_high_delay_ms"
}

append_latency_summary() {
  local scenario="$1"
  local mode="$2"
  local qos="$3"
  local payload_bytes="$4"
  local csv_file="$5"
  local log_file="$6"

  local line total success failure avg p50 p95 p99
  line="$(grep 'SUMMARY mode=' "$log_file" | tail -n 1 || true)"

  total="$(extract_summary_value "$line" "total")"
  success="$(extract_summary_value "$line" "success")"
  failure="$(extract_summary_value "$line" "failure")"
  avg="$(extract_summary_value "$line" "avg_ms")"
  p50="$(extract_summary_value "$line" "p50_ms")"
  p95="$(extract_summary_value "$line" "p95_ms")"
  p99="$(extract_summary_value "$line" "p99_ms")"

  total="${total:-0}"
  success="${success:-0}"
  failure="${failure:-0}"
  avg="${avg:-0}"
  p50="${p50:-0}"
  p95="${p95:-0}"
  p99="${p99:-0}"

  printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" \
    "$scenario" \
    "$mode" \
    "$qos" \
    "$payload_bytes" \
    "$total" \
    "$success" \
    "$failure" \
    "$avg" \
    "$p50" \
    "$p95" \
    "$p99" \
    "$csv_file" >>"$LATENCY_SUMMARY_CSV"
}

run_latency_probes() {
  LATENCY_SUMMARY_CSV="$LATENCY_DIR/latency_summary.csv"
  cat >"$LATENCY_SUMMARY_CSV" <<'EOF'
scenario,mode,qos,payload_bytes,total_samples,success_samples,failure_samples,avg_ms,p50_ms,p95_ms,p99_ms,csv_file
EOF

  local connect_csv="$LATENCY_RAW_DIR/connect_latency.csv"
  local connect_log="$LATENCY_RAW_DIR/connect_latency.log"
  local reconnect_csv="$LATENCY_RAW_DIR/reconnect_latency.csv"
  local reconnect_log="$LATENCY_RAW_DIR/reconnect_latency.log"
  local pubsub0_csv="$LATENCY_RAW_DIR/pubsub_qos0_rtt.csv"
  local pubsub0_log="$LATENCY_RAW_DIR/pubsub_qos0_rtt.log"
  local pubsub1_csv="$LATENCY_RAW_DIR/pubsub_qos1_rtt.csv"
  local pubsub1_log="$LATENCY_RAW_DIR/pubsub_qos1_rtt.log"

  (
    cd "$ROOT_DIR"
    go run ./client/probe connect --broker "$MQTT_BROKER_URL" --attempts "${CONNECT_ATTEMPTS:-600}" --concurrency "${CONNECT_CONCURRENCY:-30}" --timeout-ms "${CONNECT_TIMEOUT_MS:-5000}" --out "$connect_csv"
  ) >"$connect_log" 2>&1
  append_latency_summary "connect" "connect" "" "" "$connect_csv" "$connect_log"

  (
    cd "$ROOT_DIR"
    go run ./client/probe reconnect --broker "$MQTT_BROKER_URL" --attempts "${RECONNECT_ATTEMPTS:-500}" --timeout-ms "${RECONNECT_TIMEOUT_MS:-5000}" --gap-ms "${RECONNECT_GAP_MS:-20}" --out "$reconnect_csv"
  ) >"$reconnect_log" 2>&1
  append_latency_summary "reconnect" "reconnect" "" "" "$reconnect_csv" "$reconnect_log"

  (
    cd "$ROOT_DIR"
    go run ./client/probe pubsub --broker "$MQTT_BROKER_URL" --samples "${PUBSUB_QOS0_SAMPLES:-1500}" --qos 0 --payload-bytes "${PUBSUB_PAYLOAD_BYTES:-128}" --timeout-ms "${PUBSUB_TIMEOUT_MS:-5000}" --warmup "${PUBSUB_WARMUP:-20}" --out "$pubsub0_csv"
  ) >"$pubsub0_log" 2>&1
  append_latency_summary "pubsub_qos0" "pubsub" "0" "${PUBSUB_PAYLOAD_BYTES:-128}" "$pubsub0_csv" "$pubsub0_log"

  (
    cd "$ROOT_DIR"
    go run ./client/probe pubsub --broker "$MQTT_BROKER_URL" --samples "${PUBSUB_QOS1_SAMPLES:-1500}" --qos 1 --payload-bytes "${PUBSUB_PAYLOAD_BYTES:-128}" --timeout-ms "${PUBSUB_TIMEOUT_MS:-5000}" --warmup "${PUBSUB_WARMUP:-20}" --out "$pubsub1_csv"
  ) >"$pubsub1_log" 2>&1
  append_latency_summary "pubsub_qos1" "pubsub" "1" "${PUBSUB_PAYLOAD_BYTES:-128}" "$pubsub1_csv" "$pubsub1_log"
}

RUN_STARTED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
capture_host_info

echo "[1/4] Starting broker for load and latency probes"
start_broker

echo "[2/4] Running load baseline capture"
run_load_capture

echo "[3/4] Capturing idle baseline and latency distributions"
capture_idle_baseline
run_latency_probes

echo "[4/4] Generating plots"

if [[ ! -f "$PLOT_SCRIPT" ]]; then
  echo "Plot script not found: $PLOT_SCRIPT" >&2
  exit 1
fi

PLOT_LOG="$RESULTS_DIR/plot_generation.log"
"$PYTHON_BIN" "$PLOT_SCRIPT" \
  --latency-dir "$LATENCY_DIR" \
  --output-dir "$LATENCY_PLOTS_DIR" \
  --load-dir "$LOAD_DIR" \
  --load-output-dir "$LOAD_PLOTS_DIR" >"$PLOT_LOG" 2>&1

for required_file in \
  "$LATENCY_PLOTS_DIR/latency_cdf.png" \
  "$LATENCY_PLOTS_DIR/latency_histograms.png" \
  "$LATENCY_PLOTS_DIR/latency_percentiles.png" \
  "$LATENCY_PLOTS_DIR/idle_resource_trend.png" \
  "$LATENCY_PLOTS_DIR/plots_manifest.csv" \
  "$LOAD_PLOTS_DIR/load_tier_overview.png" \
  "$LOAD_PLOTS_DIR/load_cpu_rss_trend.png" \
  "$LOAD_PLOTS_DIR/load_plots_manifest.csv"; do
  if [[ ! -f "$required_file" ]]; then
    echo "Missing expected plot artifact: $required_file" >&2
    echo "Plot log: $PLOT_LOG" >&2
    exit 1
  fi
done

RUN_FINISHED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

MANIFEST_CSV="$RESULTS_DIR/dataset_manifest.csv"
cat >"$MANIFEST_CSV" <<'EOF'
path,type
run_info.txt,metadata
metadata/host_info.csv,metadata
metadata/git_info.txt,metadata
metadata/environment.txt,metadata
load/summary.csv,summary
load/raw/normal.csv,timeseries
load/raw/high.csv,timeseries
load/raw/very_high.csv,timeseries
load/plots/load_tier_overview.png,plot
load/plots/load_cpu_rss_trend.png,plot
load/plots/load_plots_manifest.csv,metadata
latency/latency_summary.csv,summary
latency/raw/idle_30s.csv,timeseries
latency/raw/connect_latency.csv,samples
latency/raw/reconnect_latency.csv,samples
latency/raw/pubsub_qos0_rtt.csv,samples
latency/raw/pubsub_qos1_rtt.csv,samples
latency/plots/latency_cdf.png,plot
latency/plots/latency_histograms.png,plot
latency/plots/latency_percentiles.png,plot
latency/plots/latency_percentiles.csv,summary
latency/plots/idle_resource_trend.png,plot
latency/plots/plots_manifest.csv,metadata
plot_generation.log,metadata
EOF

cat >"$RESULTS_DIR/run_info.txt" <<EOF
run_started_utc=$RUN_STARTED_UTC
run_finished_utc=$RUN_FINISHED_UTC
results_dir=$RESULTS_DIR
load_dir=$LOAD_DIR
load_plots_dir=$LOAD_PLOTS_DIR
latency_dir=$LATENCY_DIR
latency_plots_dir=$LATENCY_PLOTS_DIR
metadata_dir=$METADATA_DIR
dataset_manifest=$MANIFEST_CSV
plot_generation_log=$PLOT_LOG
mqtt_broker_addr=$MQTT_BROKER_ADDR
mqtt_broker_url=$MQTT_BROKER_URL
mqtt_info_url=$MQTT_INFO_URL
mqtt_tls_cert_file=$MQTT_TLS_CERT_FILE
mqtt_tls_key_file=$MQTT_TLS_KEY_FILE
mqtt_tls_ca_file=$MQTT_TLS_CA_FILE
mqtt_tls_session_resumption=$MQTT_TLS_SESSION_RESUMPTION
mqtt_tls_session_cache_size=$MQTT_TLS_SESSION_CACHE_SIZE
mqtt_broker_modules=$BROKER_MODULES_EFFECTIVE
mqtt_broker_modules_override=$MQTT_BROKER_MODULES
EOF

echo "Present-state capture complete."
echo "Results directory: $RESULTS_DIR"
