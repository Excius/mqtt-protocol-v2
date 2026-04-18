# MQTT-NG Project Context (Current State)

This file summarizes what has been implemented so far for modular TLS/system experimentation in this repository.

## 1. Broker modules implemented

The broker currently supports these runtime modules (`broker/cmd/main.go`):

- `baseline`
- `tls-session-resumption`
- `adaptive-tls-profiles`
- `property-validator`

Module composition behavior:

- `baseline` is mutually exclusive with other modules.
- `tls-session-resumption`, `adaptive-tls-profiles`, and `property-validator` can run independently or together.
- Non-`BALANCED` TLS profiles require `adaptive-tls-profiles`.

## 2. Adaptive TLS Profiles implementation

Implemented in `broker/modules/security/tls_profiles.go`:

- `LOW_POWER`
  - `MinVersion: TLS1.2`
  - `MaxVersion: TLS1.2`
  - cipher preference: ChaCha20-Poly1305 suites (enforced via TLS1.2)
  - curve preference favors `X25519`, `P256`
- `BALANCED`
  - `MinVersion: TLS1.2`
  - Go defaults for cipher/curve selection
- `HIGH_SECURITY`
  - `MinVersion: TLS1.3`
  - TLS1.3 cipher list set to:
    - `TLS_AES_256_GCM_SHA384`
    - `TLS_AES_128_GCM_SHA256`
  - stronger curve preference (`P521`, `P384`)

Selection path:

- CLI: `--tls-profile`
- Env fallback order: `TLS_PROFILE` → `PROFILE` → `MQTT_TLS_PROFILE`
- Startup log prints: `Using TLS Profile: <profile>`

## 3. Experiment scripts available

## `experiments/tls_profiles/run_tls_profiles_comparison.sh`

Purpose: compare `LOW_POWER/BALANCED/HIGH_SECURITY` under identical load.

Current defaults are long-run for stability:

- `CONNECT_ATTEMPTS=400`
- `CONNECT_CONCURRENCY=40`
- `LOAD_WORKERS=150`
- `LOAD_MESSAGES_PER_WORKER=12000`
- `LOAD_DELAY_MS=5`

Key outputs:

- `summary.csv`
- `raw/<profile>/...`
- `plots/*.png`
- `run_info.txt`
- `dataset_manifest.csv`

It records:

- handshake metrics
- throughput
- broker CPU/memory
- client CPU/memory
- negotiated TLS protocol/cipher/key exchange

It validates profile negotiation (e.g., HIGH_SECURITY must negotiate TLS1.3 + allowed AES ciphers + stronger key exchange).

## `experiments/tls_profiles_session_resumption/run_tls_profiles_session_resumption_comparison.sh`

Purpose: run adaptive TLS profiles with session resumption enabled together.

Behavior:

- Forces modules:
  - `adaptive-tls-profiles`
  - `tls-session-resumption`
- Uses dedicated results root by default:
  - `results/tls_profiles_session_resumption`
- Delegates to `tls_profiles` experiment script.

## `experiments/tls_resumption/run_tls_resumption_comparison.sh`

Purpose: compare no-resumption vs resumption behavior (connect/reconnect latency and reuse indicators), including plots.

## `experiments/modules/run_module_matrix.sh`

Purpose: compatibility/validation matrix across module combinations.

Scenarios currently included:

- `baseline`
- `session_only`
- `adaptive_low_power`
- `adaptive_high_security`
- `session_adaptive_low_power`

## 4. Data capture and variance-related fixes already applied

- Dynamic per-scenario TCP/WS/info ports in TLS profile experiments to avoid listener collisions.
- Combined-module wrapper fixed to avoid writing into normal `results/tls_profiles` by mistake.
- TLS negotiation probe added (protocol/cipher/key exchange) so output can be verified, not inferred.
- Client CPU average capture stabilized:
  - switched to `/proc` jiffies-delta over measured load duration (instead of relying only on sampled `ps %cpu` averages).

## 5. Typical run commands

Adaptive TLS profiles only:

```bash
PYTHON_BIN="$(pwd)/.venv/bin/python" \
bash experiments/tls_profiles/run_tls_profiles_comparison.sh
```

Adaptive TLS profiles + session resumption:

```bash
PYTHON_BIN="$(pwd)/.venv/bin/python" \
bash experiments/tls_profiles_session_resumption/run_tls_profiles_session_resumption_comparison.sh
```

Session resumption comparison:

```bash
PYTHON_BIN="$(pwd)/.venv/bin/python" \
bash experiments/tls_resumption/run_tls_resumption_comparison.sh
```

Module compatibility matrix:

```bash
bash experiments/modules/run_module_matrix.sh
```

## 6. Important interpretation note

On AES-accelerated CPUs, `BALANCED` can be close to (or occasionally better than) `LOW_POWER` for some metrics when both negotiate similar fast TLS1.3 paths.  
`HIGH_SECURITY` should generally show higher cryptographic cost when stronger key-exchange/cipher constraints are actually negotiated (which is now explicitly captured in `summary.csv`).
