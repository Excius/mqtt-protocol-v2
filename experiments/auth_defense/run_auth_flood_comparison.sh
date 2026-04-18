#!/bin/bash

# ==============================================================================
# MQTT 5 AUTH Flood Attack Experiment Runner
# ==============================================================================
# This script automates the process of testing the auth_defense module.
# It runs the broker in Baseline and Defended modes and plots the results.
# ==============================================================================

set -e

# --- Configuration & Defaults ---
ROOT_DIR=$(realpath "$(dirname "$0")/../..")
BROKER_DIR="$ROOT_DIR/broker"
EXPERIMENT_DIR="$ROOT_DIR/experiments/auth_defense"
RESULTS_DIR="$ROOT_DIR/results/auth_defense"

BROKER_BIN="$EXPERIMENT_DIR/mochi_broker"
INJECTOR_CLIENT_BIN="$EXPERIMENT_DIR/auth_injector"

# Default attack settings
ATTACK_CONCURRENCY=${ATTACK_CONCURRENCY:-50}
ATTACK_DURATION_S=${ATTACK_DURATION_S:-15}
ATTACK_TYPE=${ATTACK_TYPE:-flood}
SETTLE_DELAY_S=${SETTLE_DELAY_S:-2}

PYTHON_BIN=${PYTHON_BIN:-python3}

# --- Cleanup ---
cleanup() {
    if [ -n "$BROKER_PID" ]; then
        kill "$BROKER_PID" 2>/dev/null || true
        wait "$BROKER_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# --- Helper Functions ---
ensure_plot_dependencies() {
    if ! "$PYTHON_BIN" -c "import pandas, matplotlib" 2>/dev/null; then
        echo "matplotlib and pandas are required for plot generation. Install them with: $PYTHON_BIN -m pip install matplotlib pandas"
        exit 1
    fi
}

run_scenario() {
    local scenario_name=$1
    local module_flag=$2
    local output_dir="$RESULTS_DIR/raw/$scenario_name"
    
    mkdir -p "$output_dir"
    
    echo "Running scenario=$scenario_name modules=${module_flag:-baseline}"
    
    local broker_log="$output_dir/broker.log"
    local stats_csv="$output_dir/broker_stats.csv"
    local load_log="$output_dir/load.log"
    
    echo "timestamp_iso,broker_cpu,broker_rss" > "$stats_csv"
    
    # Start Broker
    if [ -z "$module_flag" ]; then
        "$BROKER_BIN" -tcp :48883 -ws :48882 -info :48080 > "$broker_log" 2>&1 &
    else
        "$BROKER_BIN" -tcp :48883 -ws :48882 -info :48080 -modules "$module_flag" > "$broker_log" 2>&1 &
    fi
    BROKER_PID=$!
    
    # Wait for broker to settle
    sleep "$SETTLE_DELAY_S"
    
    # Start Stats Collection in background
    local stats_pid
    (
        while kill -0 "$BROKER_PID" 2>/dev/null; do
            local ts=$(date -Iseconds)
            local raw_ps=$(ps -p "$BROKER_PID" -o %cpu,rss --no-headers 2>/dev/null)
            if [ -n "$raw_ps" ]; then
                local b_cpu=$(echo "$raw_ps" | awk '{print $1}')
                local b_rss=$(echo "$raw_ps" | awk '{print $2}')
                echo "$ts,$b_cpu,$b_rss" >> "$stats_csv"
            fi
            sleep 0.2
        done
    ) &
    stats_pid=$!
    
    # Run attack client
    "$INJECTOR_CLIENT_BIN" \
        -broker "127.0.0.1:48883" \
        -concurrency "$ATTACK_CONCURRENCY" \
        -duration "${ATTACK_DURATION_S}s" \
        -type "$ATTACK_TYPE" > "$load_log" 2>&1
        
    local load_exit_code=$?
    
    # Stop everything
    kill "$BROKER_PID" 2>/dev/null || true
    wait "$BROKER_PID" 2>/dev/null || true
    kill "$stats_pid" 2>/dev/null || true
    
    # Extract load results
    local attack_summary=$(cat "$load_log" | grep "Attack complete.")
    local total_sent=$(echo "$attack_summary" | grep -oP 'Packets Sent: \K\d+' || echo "0")
    local total_connects=$(echo "$attack_summary" | grep -oP 'Connections Made: \K\d+' || echo "0")
    local total_errors=$(echo "$attack_summary" | grep -oP 'Errors: \K\d+' || echo "0")
    
    # Calculate CPU/Memory aggregates (skip first 3 warmup samples)
    local peak_cpu=$(awk -F',' 'NR>4 {if ($2>max) max=$2} END {print max}' "$stats_csv")
    local peak_mem_kb=$(awk -F',' 'NR>4 {if ($3>max) max=$3} END {print max}' "$stats_csv")
    local peak_mem_mb=$(echo "$peak_mem_kb / 1024" | bc -l)
    
    if [ -z "$peak_cpu" ]; then peak_cpu="0"; fi
    if [ -z "$peak_mem_mb" ]; then peak_mem_mb="0"; fi
    
    printf "%s,%s,%.3f,%.3f,%s,%s,%s,%s,%s,%s\n" \
        "$scenario_name" "${module_flag:-baseline}" "$peak_cpu" "$peak_mem_mb" \
        "$total_connects" "$total_sent" "$total_errors" \
        "$broker_log" "$stats_csv" "$load_log" >> "$SUMMARY_CSV"
}

# --- Main Execution ---
rm -rf "$RESULTS_DIR"
mkdir -p "$RESULTS_DIR/raw"

SUMMARY_CSV="$RESULTS_DIR/summary.csv"
echo "scenario,modules,broker_cpu_peak_pct,broker_mem_peak_mib,total_connects,total_sent,total_errors,broker_log,broker_stats_csv,load_log" > "$SUMMARY_CSV"

echo "Building binaries..."
(cd "$BROKER_DIR" && go build -o "$BROKER_BIN" ./cmd/main.go)
(cd "$ROOT_DIR" && go build -o "$INJECTOR_CLIENT_BIN" ./client/auth_injector/main.go)

ensure_plot_dependencies

# Run Baseline
run_scenario "baseline_no_defense" ""

# Run With Defense
run_scenario "with_defense" "auth-defense"

# Plot
"$PYTHON_BIN" "$EXPERIMENT_DIR/plot_auth_defense.py" \
    --summary-csv "$SUMMARY_CSV" \
    --raw-dir "$RESULTS_DIR/raw" \
    --output-dir "$RESULTS_DIR"

echo "Auth defense experiment complete."
echo "Results directory: $RESULTS_DIR"
