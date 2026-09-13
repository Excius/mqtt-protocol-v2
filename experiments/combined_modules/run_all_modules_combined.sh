#!/usr/bin/env bash
#
# Enables every module that can safely layer together on the same TCP
# listener at once (TLS session resumption, adaptive TLS profiles, message
# integrity, priority messaging, wildcard tokens) and measures the
# *combined* overhead against the pure MQTTv5 baseline from
# experiments/baseline/run_baseline_only.sh.
#
# quic-transport, ebpf-filter, auth-defense, and property-validator are
# deliberately left out of this combo:
#   - quic-transport swaps the transport itself (UDP/QUIC instead of TCP),
#     so it isn't something you "add on top" of a TCP comparison — it has
#     its own dedicated experiment (experiments/quic_transport).
#   - ebpf-filter bans source IPs that open connections too quickly, and
#     auth-defense's default per-IP limiter (5 new connections/sec) rejects
#     new connections just as fast. The high/very_high load tiers in this
#     harness intentionally open many concurrent connections from
#     127.0.0.1, which trips both limiters and self-blocks the very load
#     generator being used to measure the other modules (confirmed by
#     running this script: auth-defense alone turns every subsequent
#     connect/pubsub probe into "rate limit exceeded" / EOF failures).
#   - property-validator's default per-client cumulative budget
#     (MaxClientBudget, 32KB) is meant to catch an attacker who floods huge
#     properties, but it counts ALL user-property bytes for the life of a
#     connection with no decay. A single long-lived client that legitimately
#     carries a "priority" tag (priority-messaging) and an
#     "integrity-signature" (message-integrity) on every publish — exactly
#     what this combo exercises — burns through 32KB in well under 500
#     messages, at which point every further publish from that client is
#     permanently rejected via packets.ErrRejectPacket for the rest of the
#     connection. Mochi-mqtt's core sends no PUBACK at all for a QoS>0
#     publish rejected this way, so every subsequent publish from every
#     load worker stalls waiting for an ack that never comes — confirmed by
#     actually running this combo: the "high" load tier (60 workers x 800
#     msgs) took over 30 minutes and was still climbing at ~5 msgs/sec
#     system-wide (each rejected publish burns a multi-second client-side
#     timeout) instead of finishing in ~10 seconds. This isn't a property-
#     validator bug in isolation — it only shows up once another module adds
#     legitimate per-message properties — but it means property-validator's
#     current defaults cannot coexist with sustained per-message-signed or
#     per-message-prioritized traffic. It has its own dedicated experiment
#     with an adversarial client tuned to trip the budget on purpose
#     (experiments/ebpf_filter, experiments/auth_defense,
#     experiments/property_validator).
#
# Usage:
#   ./experiments/combined_modules/run_all_modules_combined.sh

set -euo pipefail
export PYTHON_BIN="${PYTHON_BIN:-$HOME/.venv/bin/python}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EXP_NAME="all_modules_combined"
RESULTS_DIR="$ROOT_DIR/results/$EXP_NAME"

CERT_DIR="${TLS_CERT_DIR:-$ROOT_DIR/experiments/baseline/certs}"
GENERATE_CERTS_SCRIPT="$ROOT_DIR/experiments/baseline/generate_tls_certs.sh"
MQTT_TLS_CERT_FILE="${MQTT_TLS_CERT_FILE:-$CERT_DIR/server.cert.pem}"
MQTT_TLS_KEY_FILE="${MQTT_TLS_KEY_FILE:-$CERT_DIR/server.key.pem}"
MQTT_TLS_CA_FILE="${MQTT_TLS_CA_FILE:-$CERT_DIR/ca.cert.pem}"

if [[ ! -f "$MQTT_TLS_CERT_FILE" || ! -f "$MQTT_TLS_KEY_FILE" || ! -f "$MQTT_TLS_CA_FILE" ]]; then
  echo "Generating TLS certificates in $CERT_DIR..."
  mkdir -p "$CERT_DIR"
  "$GENERATE_CERTS_SCRIPT" "$CERT_DIR"
fi

export MQTT_TLS_CERT_FILE
export MQTT_TLS_KEY_FILE
export MQTT_TLS_CA_FILE
export MQTT_TLS_INSECURE_SKIP_VERIFY="${MQTT_TLS_INSECURE_SKIP_VERIFY:-false}"
export MQTT_TLS_SERVER_NAME="${MQTT_TLS_SERVER_NAME:-localhost}"

COMBINED_MODULES="tls-session-resumption,adaptive-tls-profiles,message-integrity,priority-messaging,wildcard-tokens"

echo "=== Running Baseline (Control) ==="
"$ROOT_DIR/experiments/baseline/run_baseline_only.sh" "$RESULTS_DIR/baseline"

echo "=== Running All Modules Combined (Variable) ==="
echo "Modules: $COMBINED_MODULES"
export MQTT_BROKER_MODULES="$COMBINED_MODULES"
export MQTT_TLS_SESSION_RESUMPTION="true"
export TLS_PROFILE="${TLS_PROFILE:-BALANCED}"
export MQTT_INTEGRITY_SECRET="default-broker-secret"
export MQTT_PRIORITY="High"
# The broker now terminates TLS on its TCP listener (tls-session-resumption
# and adaptive-tls-profiles only do anything once a cert is configured), so
# the load/probe clients must dial with a TLS scheme instead of the
# run_present_state_capture.sh default of tcp://127.0.0.1:1883.
export MQTT_BROKER_URL="tls://127.0.0.1:1883"

# The wildcard-tokens hook only does meaningful ACL work once something
# actually subscribes with a wildcard filter. The broker isn't up yet at
# this point and the subscriber has no reconnect loop of its own (it dials
# once and exits on failure), so keep retrying an "admin" subscription to
# "#" in the background until the module capture finishes.
SUB_STOP_FILE="$(mktemp)"
rm -f "$SUB_STOP_FILE"
(
  cd "$ROOT_DIR"
  while [[ ! -f "$SUB_STOP_FILE" ]]; do
    # set -e is inherited into this subshell; without `|| true` the very
    # first attempt (which normally fails because the broker isn't up yet)
    # would trigger errexit and kill this whole retry loop on the spot,
    # silently, since stderr is discarded below.
    MQTT_BROKER_URL="tls://127.0.0.1:1883" MQTT_CLIENT_ID="admin" MQTT_TOPIC="#" \
      go run ./client/subscriber >/dev/null 2>&1 || true
    sleep 1
  done
) &
SUB_LOOP_PID=$!

cleanup_subscriber() {
  touch "$SUB_STOP_FILE"
  pkill -P "$SUB_LOOP_PID" 2>/dev/null || true
  kill "$SUB_LOOP_PID" 2>/dev/null || true
  wait "$SUB_LOOP_PID" 2>/dev/null || true
  rm -f "$SUB_STOP_FILE"
}
trap cleanup_subscriber EXIT

"$ROOT_DIR/experiments/baseline/run_present_state_capture.sh" "$RESULTS_DIR/module" || true

cleanup_subscriber
trap - EXIT

echo "=== Generating Comparison Plots ==="
"$PYTHON_BIN" "$ROOT_DIR/experiments/plot_comparison.py" \
    --baseline-dir "$RESULTS_DIR/baseline" \
    --module-dir "$RESULTS_DIR/module" \
    --module-name "All Modules Combined" \
    --output-dir "$RESULTS_DIR/comparison_plots"

echo "Done! Comparison plots saved to $RESULTS_DIR/comparison_plots"
