# MQTT-NG — Paper Context (Transport & System-Level Enhancements)

## Project Summary

This project focuses on designing and implementing **transport and system-level improvements for MQTT brokers** used in IoT systems.

The goal is to build a **modular, high-performance MQTT architecture (MQTT-NG)** that improves:

- connection latency
- reconnection efficiency
- system scalability
- resource utilization (CPU & memory)

The implementation is based on a **Go-based MQTT broker (Mochi-MQTT)**.

---

## Scope

This project strictly covers:

### Included

- Transport improvements (QUIC)
- TLS optimization (session resumption)
- TLS offloading (reverse proxy)
- Kernel-level filtering (eBPF/XDP)
- Broker-level rate limiting
- System performance evaluation

---

## Current System (Baseline Setup)

### Broker

- Mosquitto MQTT broker
- Used for initial performance evaluation

### Clients

- Python-based clients using Paho MQTT
- Publisher and Subscriber setup

### Evaluated Components

- TLS handshake latency
- TLS session resumption
- connection scalability

---

## Target System (MQTT-NG)

### Broker Platform

- Mochi-MQTT (Go-based)
- Lightweight and modular architecture

### Client Environment

- Go-based MQTT clients using Paho library

---

## System Architecture

```
Client
↓
Transport Layer (TCP / QUIC)
↓
TLS Layer (Session Resumption)
↓
MQTT Broker (Mochi)
↓
Modules (Rate Limiter / eBPF / Monitoring)
```

---

## Core Improvements (Paper A)

### 1. MQTT over QUIC

- Replace TCP transport with QUIC
- Faster connection establishment
- Reduced head-of-line blocking
- Better performance in unstable networks

---

### 2. TLS Offloading via Reverse Proxy

- Move TLS termination from broker to a reverse proxy (e.g., NGINX/HAProxy)
- Broker handles only plain MQTT traffic internally
- Reduces CPU load on broker
- Improves scalability under high connection load
- Common in real-world MQTT deployments

---

### 3. TLS Session Resumption

- Reuse previously established TLS sessions
- Avoid full handshake on reconnect
- Reduce latency and CPU usage
- Supports load-balanced deployments

---

### 4. Kernel-Level Packet Filtering (eBPF/XDP)

- Inspect packets at kernel level
- Drop malformed or malicious traffic early
- Reduce load on broker
- Improve resilience under high traffic

---

### 5. Built-in Rate Limiting

- Token bucket algorithm
- Per-client and per-topic limits
- Prevent system overload
- Maintain stable performance

---

### 6. User-Property Injection Defense

- Broker-side validation for MQTT 5 User Properties
- Property count limit — restricts the maximum number of user properties per message
- Key/value size limits — prevents excessively large metadata fields
- Packet property budget — limits total metadata size in a single message
- Per-client cumulative quota — prevents repeated property abuse from the same client
- Allows the broker to reject malicious packets early, protecting memory and CPU resources

---

### 7. AUTH Flood Mitigation

- Broker-side defenses to control MQTT 5 authentication traffic and prevent flooding
- Connection rate limiting — restricts the number of new connection attempts per client/IP
- AUTH packet validation — limits or blocks excessive AUTH packets during a session
- Connection timeout controls — terminates stalled or suspicious authentication attempts
- Concurrent connection limits — prevents attackers from opening large numbers of parallel sessions
- Detects and blocks abnormal authentication behavior early, protecting system resources

---

## Repository Structure

```
mqtt-ng/
├─ broker/ # mochi-mqtt base
├─ client/
│ ├─ publisher/
│ ├─ subscriber/
│ ├─ load/
├─ modules/
│ ├─ transport/ # QUIC
│ ├─ security/ # TLS
│ ├─ defense/ # eBPF
│ ├─ qos/ # rate limiter
├─ experiments/
├─ docs/
```

---

## Development Plan

### Phase 1 — Baseline System

- Setup broker and clients
- Implement publish/subscribe
- Measure baseline latency and resource usage

---

### Phase 2 — TLS Improvements

- Implement TLS baseline
- Add TLS offloading setup
- Add session resumption
- Measure handshake improvements

---

### Phase 3 — Rate Limiting

- Implement token bucket in broker
- Apply per-client limits
- Evaluate performance under load

---

### Phase 4 — Kernel-Level Filtering

- Develop eBPF/XDP programs
- Attach to network interface
- Filter traffic before broker

---

### Phase 5 — QUIC Transport

- Implement QUIC listener
- Map MQTT sessions to QUIC streams
- Compare with TCP performance

---

### Phase 6 — User-Property Injection Defense

- Implement broker-side user property validation
- Apply count, key/value size, and packet budget limits
- Apply per-client cumulative quota
- Evaluate memory and CPU impact under property-abuse load

---

### Phase 7 — AUTH Flood Mitigation

- Implement connection rate limiting per client/IP
- Add AUTH packet validation and session limits
- Apply connection timeout and concurrent session controls
- Evaluate resilience under AUTH flood attack scenarios

---

## Experimental Setup

### Baseline

- MQTT over TCP
- Standard TLS
- Mochi broker default configuration

### Enhanced System

- QUIC transport
- TLS offloading via proxy
- TLS session resumption
- eBPF filtering
- Rate limiting

---

## Evaluation Metrics

### Performance

- connection latency
- TLS handshake time
- reconnection latency
- message throughput

### System

- CPU utilization
- memory usage

### Stability

- performance under concurrent clients
- behaviour under high load

---

## Final Goal

To demonstrate that:

> Transport and system-level improvements can significantly enhance MQTT broker performance, scalability, and efficiency in large-scale IoT deployments.

---

## Key Principles

- modular design
- measurable improvements
- backward compatibility
- reproducible experiments

## Implementation Rule (Mandatory)

- Any new improvement must be implemented as an independent, composable module.
- New modules must remain compatible with existing modules and baseline mode.
- Every change must include compatibility testing across relevant module combinations (not only isolated single-module tests).
