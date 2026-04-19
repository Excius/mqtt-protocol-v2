# MQTT-NG Project Context (Current State)

This document is a code-accurate snapshot of what is currently implemented in this repository.

It focuses on:

- Runtime broker modules and their exact behavior
- How modules are composed and enforced at startup
- Current experiment runners and generated artifacts
- Supporting client tools used by experiments
- What is present vs what is still not implemented

---

## 1. Runtime module system (actual broker behavior)

The broker module entrypoint is `broker/cmd/main.go`.

Supported module names:

- `baseline`
- `tls-session-resumption`
- `adaptive-tls-profiles`
- `property-validator`
- `auth-defense`

### 1.1 Module parsing and composition rules

Runtime module selection comes from `--modules` (comma-separated).

Rules:

- `baseline` is exclusive and cannot be combined with any other module.
- If only `baseline` is passed, runtime behaves as "no extra modules enabled".
- Unknown module names are rejected at startup.
- Duplicate names are deduplicated.
- If `--modules` is provided but resolves to nothing valid, startup errors.

Legacy behavior when `--modules` is omitted:

- `--tls-session-resumption` flag still exists (default: `true`).
- If omitted modules + legacy flag true -> runtime implicitly enables `tls-session-resumption`.
- If omitted modules + legacy flag false -> runtime runs with no optional modules.

### 1.2 TLS profile guardrail

- TLS profile is normalized from `--tls-profile` (or env fallback).
- If `adaptive-tls-profiles` is NOT enabled, only `BALANCED` is allowed.
- Requesting `LOW_POWER` or `HIGH_SECURITY` without `adaptive-tls-profiles` causes startup failure.

### 1.3 TLS profile env fallback order

When `--tls-profile` is not explicitly set:

1. `TLS_PROFILE`
2. `PROFILE`
3. `MQTT_TLS_PROFILE`
4. default `BALANCED`

---

## 2. Module deep details

## 2.1 `baseline`

`baseline` is a control mode marker, not a standalone hook/plugin.

Effect:

- Disables optional module activation (`property-validator`, `auth-defense`, adaptive profile selection logic, resumption toggle module).
- With TLS enabled, session ticket support remains off unless `tls-session-resumption` module is active.

## 2.2 `tls-session-resumption`

Implemented via runtime wiring in `broker/cmd/main.go` + TLS config generation.

Behavior:

- Activates `runtime.tlsSessionResumption = true`.
- When TLS is enabled (`--tls-cert-file` and `--tls-key-file`), broker sets:
  - `tls.Config.SessionTicketsDisabled = false`
- Without this module (or when baseline), broker sets:
  - `tls.Config.SessionTicketsDisabled = true`

Scope note:

- This module only affects TLS listener behavior when TLS files are provided.
- If broker is running plain TCP (no cert/key), this module has no effect.

## 2.3 `adaptive-tls-profiles`

Core implementation: `broker/modules/security/tls_profiles.go`.

Supported profiles:

- `LOW_POWER`
  - `MinVersion = TLS1.2`
  - `MaxVersion = TLS1.2`
  - Cipher suites constrained to ChaCha20:
    - `TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305`
    - `TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305`
  - Curve preference: `X25519`, `P256`

- `BALANCED`
  - `MinVersion = TLS1.2`
  - Leaves cipher/curve selection to Go defaults

- `HIGH_SECURITY`
  - `MinVersion = TLS1.3`
  - Cipher list constrained to:
    - `TLS_AES_256_GCM_SHA384`
    - `TLS_AES_128_GCM_SHA256`
  - Curve preference: `P521`, `P384`

Normalization behavior:

- Case-insensitive input accepted via uppercase normalization.
- Empty value maps to `BALANCED`.
- Invalid values fail with explicit supported profile list.

## 2.4 `property-validator`

Implementation: `broker/modules/defense/property_validator.go`.

Hook identity and coverage:

- Hook ID: `user-property-validator`
- Hook points: `OnPublish`, `OnDisconnect`
- Target packet type: MQTT PUBLISH user properties (`pk.Properties.User`)

Enforcement checks (in execution order):

1. Property count per packet (`MaxProperties`)
2. Key byte-length per property (`MaxKeySize`)
3. Value byte-length per property (`MaxValueSize`)
4. Total property payload per packet (`MaxPropertyPayload`)
5. Per-client cumulative property budget (`MaxClientBudget`)

Default limits:

- `MaxProperties = 10`
- `MaxKeySize = 256`
- `MaxValueSize = 256`
- `MaxPropertyPayload = 4096` bytes
- `MaxClientBudget = 32768` bytes

Operational details:

- Per-client cumulative budget stored in `sync.Map` keyed by `client.ID`.
- Budget counters are atomic (`*int64` per client).
- On disconnect, client budget is deleted (budget resets on reconnect).
- On violation, returns `packets.ErrRejectPacket`.
- Tracks metrics via atomics:
  - `PacketsChecked`
  - `PacketsDropped`
  - `ViolationCount`

Configuration path today:

- Broker currently adds this hook with `nil` config in `main.go`.
- That means defaults are active unless code is modified to pass custom config.

## 2.5 `auth-defense`

Implementation: `broker/modules/defense/auth_defense.go`.

Hook identity and coverage:

- Hook ID: `auth-defense`
- Hook points: `OnConnect`, `OnDisconnect`, `OnAuthPacket`, `OnSessionEstablished`

Protection mechanisms:

1. Global concurrent connection cap (`MaxConcurrentConn`)
2. Per-IP new connection rate cap per second (`MaxConnPerSec`)
3. Per-session AUTH packet cap (`MaxAuthPerConn`)
4. Authentication completion timeout (`ConnTimeout`)

Default limits:

- `MaxConnPerSec = 5`
- `MaxConcurrentConn = 20`
- `MaxAuthPerConn = 2`
- `ConnTimeout = 30s`

Implementation details:

- Active connection count uses atomic int64.
- Per-IP rate tracker uses `sync.Map` + per-IP mutex and 1-second rolling reset window.
- Per-client auth state stores:
  - `authCount`
  - auth timeout timer
  - authenticated flag (`isAuthed`)
- Timer callback forcibly stops client if session not established before timeout.
- AUTH overflow or missing state path returns `packets.ErrRejectPacket`.
- Metrics:
  - `AuthPacketsReceived`
  - `AuthPacketsBlocked`
  - `ConnectionsRejected`
  - `AuthViolationCount`

Configuration path today:

- Broker adds this hook with `nil` config in `main.go`.
- Therefore default thresholds are active unless code is changed to inject config.

---

## 3. Broker runtime wiring beyond module flags

Current startup sequence (important for interpretation):

- Always adds allow-all auth hook (`auth.AllowHook`) first.
- Conditionally adds `property-validator` hook.
- Conditionally adds `auth-defense` hook.
- Starts listeners:
  - TCP (TLS optional)
  - WebSocket (plain)
  - HTTP stats/info endpoint

This means module defenses are currently implemented as hook-level controls in the same broker process, not as external sidecars.

---

## 4. Current experiments (what exists now)

## 4.1 Baseline present-state capture

Script: `experiments/baseline/run_present_state_capture.sh`

Purpose:

- Capture baseline idle/load/latency/resource behavior with rich host + broker telemetry.

Artifacts:

- Timestamped result root under `results/present_state_*`
- Structured subfolders for latency, load, plots, metadata

## 4.2 TLS resumption comparison

Script: `experiments/tls_resumption/run_tls_resumption_comparison.sh`

Purpose:

- Compare `old_no_resumption` vs `new_with_resumption` using connect/reconnect probes.
- Includes OpenSSL session reuse check (`Reused, TLS ...`).

Scenario module defaults:

- old: `baseline`
- new: `tls-session-resumption`
- overridable by `MQTT_BROKER_MODULES`

## 4.3 Adaptive TLS profile comparison

Script: `experiments/tls_profiles/run_tls_profiles_comparison.sh`

Purpose:

- Compare `LOW_POWER`, `BALANCED`, `HIGH_SECURITY` under common load.

Key behavior:

- Uses dynamic per-profile port allocation to avoid collisions.
- Probes negotiated TLS protocol/cipher/key exchange via OpenSSL.
- Enforces expected negotiation constraints for LOW_POWER/HIGH_SECURITY.
- Records broker and client CPU/RSS, including jiffies-based CPU averaging.

Default module set:

- `TLS_PROFILE_MODULES=adaptive-tls-profiles`
- optional `TLS_PROFILE_EXTRA_MODULES` appended

## 4.4 Combined TLS profiles + resumption

Script: `experiments/combined_modules/run_tls_profiles_resumption_combined.sh`

Purpose:

- Run profile comparison with both modules active:
  - `adaptive-tls-profiles`
  - `tls-session-resumption`

Default output root:

- `results/tls_profiles_resumption_combined`

## 4.5 Property validator defense experiment

Script: `experiments/property_validator/run_property_validator_comparison.sh`

Scenarios:

- `baseline_no_defense` (`baseline`)
- `with_defense` (`property-validator`)

Attack generator:

- `client/property_injector`

Metrics:

- Throughput proxy from injector summary
- Broker CPU peak and memory peak

## 4.6 AUTH flood defense experiment

Script: `experiments/auth_defense/run_auth_flood_comparison.sh`

Scenarios:

- `baseline_no_defense`
- `with_defense` (`auth-defense`)

Attack generator:

- `client/auth_injector`

Supported attack modes:

- `flood` (AUTH packet flood)
- `slowloris` (open connections without auth progression)

## 4.7 Combined defense experiment (AUTH + property)

Script: `experiments/combined_modules/run_auth_property_defense_comparison.sh`

Scenarios:

- `auth_baseline` (`baseline`)
- `auth_combined_defense` (`auth-defense,property-validator`)
- `property_baseline` (`baseline`)
- `property_combined_defense` (`auth-defense,property-validator`)

Generates merged overview plots for both attack classes.

---

## 5. Client tools currently in repository

## 5.1 Functional clients

- `client/publisher`
  - Basic publish loop client (optional TLS via env).
- `client/subscriber`
  - Basic subscribe client (optional TLS via env).

## 5.2 Load and probe clients

- `client/load`
  - Multi-worker publish load generator.
  - Optional periodic reconnect during load (`reconnect_every`).
  - Emits machine-readable `SUMMARY ...` line consumed by scripts.

- `client/probe`
  - `connect` mode: connection setup latency.
  - `reconnect` mode: first-connect vs reconnect latency and speedup.
  - `pubsub` mode: end-to-end RTT latency.
  - Writes CSV outputs + stdout summaries consumed by experiment wrappers.

## 5.3 Adversarial clients

- `client/property_injector`
  - Sends retain=true publish traffic with large user properties.
  - Intended to stress metadata processing/memory paths.

- `client/auth_injector`
  - Raw packet sender for MQTT5 CONNECT + AUTH flood patterns.
  - Also supports slowloris-like connection holding mode.

Note: `client/attack_property/` directory currently exists but is empty.

---

## 6. What changed vs older context files

The following older references are no longer current and have been superseded:

- `experiments/tls_profiles_session_resumption/run_tls_profiles_session_resumption_comparison.sh`
  - replaced by `experiments/combined_modules/run_tls_profiles_resumption_combined.sh`

- `experiments/modules/run_module_matrix.sh`
  - not present in current tree

Also, module inventory now includes `auth-defense` in addition to prior TLS/property modules.

---

## 7. Current implementation boundaries

Implemented and runnable now:

- Adaptive TLS profile module
- TLS session ticket/resumption toggle module
- User-property validation defense module
- AUTH flood/slow-auth defense module
- Combined-module experiment runners and plotting pipelines

Not yet represented as active runtime modules in this codebase snapshot:

- QUIC transport runtime path
- TLS offloading proxy integration module
- eBPF/XDP kernel filter runtime module

Repository has module namespace directories for security/defense work, but current runtime module set is the five modules listed in Section 1.
