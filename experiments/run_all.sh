#!/usr/bin/env bash



export EQUAL_TIER_DURATION_S=5
export NORMAL_WORKERS=10
export NORMAL_DELAY_MS=20
export HIGH_WORKERS=50
export HIGH_DELAY_MS=10
export VERY_HIGH_WORKERS=100
export VERY_HIGH_DELAY_MS=5

export CONNECT_ATTEMPTS=50
export RECONNECT_ATTEMPTS=50
export PUBSUB_QOS0_SAMPLES=100
export PUBSUB_QOS1_SAMPLES=100
export IDLE_DURATION_S=5

mkdir -p results

echo "Running baseline (pure MQTTv5 reference)..."
./experiments/baseline/run_baseline_only.sh > results/baseline_mqttv5_run.log 2>&1

echo "Running tls_resumption..."
./experiments/tls_resumption/run_tls_resumption_comparison.sh > results/tls_resumption_run.log 2>&1

echo "Running tls_profiles..."
./experiments/tls_profiles/run_tls_profiles_comparison.sh > results/tls_profiles_run.log 2>&1

echo "Running property_validator..."
./experiments/property_validator/run_property_validator_comparison.sh > results/property_validator_run.log 2>&1

echo "Running auth_defense..."
./experiments/auth_defense/run_auth_flood_comparison.sh > results/auth_defense_run.log 2>&1

echo "Running message_integrity..."
./experiments/message_integrity/run_experiment.sh > results/message_integrity_run.log 2>&1

echo "Running priority_messaging..."
./experiments/priority_messaging/run_experiment.sh > results/priority_messaging_run.log 2>&1

echo "Running wildcard_tokens..."
./experiments/wildcard_tokens/run_experiment.sh > results/wildcard_tokens_run.log 2>&1

echo "Running quic_transport..."
./experiments/quic_transport/run_experiment.sh > results/quic_transport_run.log 2>&1

echo "Running ebpf_filter..."
./experiments/ebpf_filter/run_experiment.sh > results/ebpf_filter_run.log 2>&1

echo "Running combined_modules (auth+property defense)..."
./experiments/combined_modules/run_auth_property_defense_comparison.sh > results/combined_auth_property_run.log 2>&1

echo "Running combined_modules (tls profiles+resumption)..."
./experiments/combined_modules/run_tls_profiles_resumption_combined.sh > results/combined_tls_run.log 2>&1

echo "Running combined_modules (all modules combined)..."
./experiments/combined_modules/run_all_modules_combined.sh > results/combined_all_modules_run.log 2>&1

echo "ALL DONE!"
