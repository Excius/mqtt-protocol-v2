#!/usr/bin/env python3
"""Generates the curated set of summary graphs referenced from
MODULES_DEEP_DIVE.md, reading directly from the raw CSVs each experiment
script already produced under results/. Output goes to docs/graphs/ (tracked
in git, unlike the raw results/ data).

Run after the experiments have been executed at least once:
    $HOME/.venv/bin/python experiments/generate_summary_graphs.py
"""

import csv
import os
import statistics
import sys
from pathlib import Path

import matplotlib.pyplot as plt
import matplotlib.ticker as mticker

ROOT = Path(__file__).resolve().parent.parent
RESULTS = ROOT / "results"
OUT_DIR = ROOT / "docs" / "graphs"
OUT_DIR.mkdir(parents=True, exist_ok=True)

# --- Validated palette (dataviz skill reference instance, light mode) ------
BLUE = "#2a78d6"
ORANGE = "#eb6834"
AQUA = "#1baf7a"
YELLOW = "#eda100"
MAGENTA = "#e87ba4"
GREEN = "#008300"
VIOLET = "#4a3aa7"
RED = "#e34948"

SURFACE = "#fcfcfb"
INK_PRIMARY = "#0b0b0b"
INK_SECONDARY = "#52514e"
INK_MUTED = "#898781"
GRID = "#e1e0d9"
BASELINE_AXIS = "#c3c2b7"

STATUS_GOOD = "#0ca30c"
STATUS_CRITICAL = "#d03b3b"

plt.rcParams.update({
    "font.family": "sans-serif",
    "font.sans-serif": ["DejaVu Sans", "Arial", "sans-serif"],
    "figure.facecolor": SURFACE,
    "axes.facecolor": SURFACE,
    "axes.edgecolor": BASELINE_AXIS,
    "axes.labelcolor": INK_SECONDARY,
    "axes.titlecolor": INK_PRIMARY,
    "text.color": INK_PRIMARY,
    "xtick.color": INK_MUTED,
    "ytick.color": INK_MUTED,
    "grid.color": GRID,
    "axes.grid": True,
    "axes.grid.axis": "y",
    "grid.linewidth": 0.8,
    "axes.axisbelow": True,
    "axes.spines.top": False,
    "axes.spines.right": False,
    "axes.spines.left": False,
    "font.size": 11,
})


def read_csv(path):
    with open(path, "r", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def savefig(fig, name, tight=True):
    path = OUT_DIR / name
    if tight:
        fig.tight_layout()
    fig.savefig(path, dpi=160, facecolor=SURFACE)
    plt.close(fig)
    print(f"wrote {path.relative_to(ROOT)}")


def bar_labels(ax, bars, fmt="{:.1f}", color=INK_PRIMARY, offset_frac=0.02):
    ymin, ymax = ax.get_ylim()
    span = ymax - ymin if ymax > ymin else 1
    for b in bars:
        h = b.get_height()
        ax.text(
            b.get_x() + b.get_width() / 2,
            h + span * offset_frac,
            fmt.format(h),
            ha="center", va="bottom", fontsize=9.5, color=color,
        )


# ---------------------------------------------------------------------------
# 1. Baseline reference latency
# ---------------------------------------------------------------------------
def chart_baseline_reference():
    rows = read_csv(RESULTS / "baseline_mqttv5" / "latency" / "latency_summary.csv")
    labels = {"connect": "Connect", "reconnect": "Reconnect", "pubsub_qos0": "Pub/Sub QoS0", "pubsub_qos1": "Pub/Sub QoS1"}
    scenarios = [r["scenario"] for r in rows]
    p50 = [float(r["p50_ms"]) for r in rows]
    p95 = [float(r["p95_ms"]) for r in rows]
    p99 = [float(r["p99_ms"]) for r in rows]

    x = range(len(scenarios))
    width = 0.26
    fig, ax = plt.subplots(figsize=(8.5, 5))
    b1 = ax.bar([i - width for i in x], p50, width, label="p50", color=BLUE)
    b2 = ax.bar(list(x), p95, width, label="p95", color=ORANGE)
    b3 = ax.bar([i + width for i in x], p99, width, label="p99", color=AQUA)
    ax.set_yscale("log")
    ax.set_ylabel("Latency (ms, log scale)")
    ax.set_xticks(list(x))
    ax.set_xticklabels([labels[s] for s in scenarios])
    ax.set_title("Pure MQTTv5 baseline — the reference every module is measured against")
    ax.set_ylim(top=ax.get_ylim()[1] * 3.5)
    ax.legend(frameon=False, ncols=3, loc="upper right")
    for bars in (b1, b2, b3):
        bar_labels(ax, bars, fmt="{:.3f}", offset_frac=0.12)
    savefig(fig, "01_baseline_reference_latency.png")


# ---------------------------------------------------------------------------
# 2. TLS session resumption speedup
# ---------------------------------------------------------------------------
def chart_tls_resumption():
    rows = read_csv(RESULTS / "tls_resumption" / "summary.csv")
    by_scenario = {r["scenario"]: r for r in rows}
    old = by_scenario["old_no_resumption"]
    new = by_scenario["new_with_resumption"]

    scenarios = ["Without resumption", "With resumption"]
    first = [float(old["reconnect_first_avg_ms"]), float(new["reconnect_first_avg_ms"])]
    reconnect = [float(old["reconnect_avg_ms"]), float(new["reconnect_avg_ms"])]
    speedup = [float(old["reconnect_speedup_x"]), float(new["reconnect_speedup_x"])]

    x = range(len(scenarios))
    width = 0.32
    fig, ax = plt.subplots(figsize=(7.5, 5))
    b1 = ax.bar([i - width / 2 for i in x], first, width, label="First connect (full handshake)", color=BLUE)
    b2 = ax.bar([i + width / 2 for i in x], reconnect, width, label="Reconnect", color=ORANGE)
    ax.set_ylabel("Latency (ms)")
    ax.set_xticks(list(x))
    ax.set_xticklabels(scenarios)
    ax.set_title("TLS session resumption: reconnect speedup is real")
    ax.legend(frameon=False, loc="upper right")
    bar_labels(ax, b1, fmt="{:.2f} ms")
    bar_labels(ax, b2, fmt="{:.2f} ms")
    for i, s in enumerate(speedup):
        ax.text(i, max(first[i], reconnect[i]) * 1.28, f"{s:.2f}× speedup", ha="center", fontsize=10, color=STATUS_GOOD if s > 1 else INK_SECONDARY, fontweight="bold")
    ax.set_ylim(0, max(first + reconnect) * 1.5)
    savefig(fig, "02_tls_resumption_speedup.png")


# ---------------------------------------------------------------------------
# 3. Adaptive TLS profiles handshake cost + negotiated cipher
# ---------------------------------------------------------------------------
def chart_tls_profiles():
    import glob
    matches = glob.glob(str(RESULTS / "tls_profiles" / "**" / "summary.csv"), recursive=True)
    if not matches:
        print("skip tls_profiles: no summary.csv found")
        return
    rows = read_csv(matches[0])
    order = ["LOW_POWER", "BALANCED", "HIGH_SECURITY"]
    rows = sorted(rows, key=lambda r: order.index(r["profile"]) if r["profile"] in order else 99)

    profiles = [r["profile"] for r in rows]
    handshake = [float(r["handshake_avg_ms"]) for r in rows]
    ciphers = [r["negotiated_cipher"] for r in rows]
    curves = [r["negotiated_key_exchange"].split(";")[0].strip() if r["negotiated_key_exchange"] not in ("na", "") else "" for r in rows]

    fig, ax = plt.subplots(figsize=(8, 5.2))
    bars = ax.bar(profiles, handshake, color=[AQUA, BLUE, VIOLET], width=0.55)
    ax.set_ylabel("Handshake latency (ms, avg)")
    ax.set_title("Adaptive TLS profiles — real negotiated cipher per profile")
    bar_labels(ax, bars, fmt="{:.1f} ms", offset_frac=0.12)
    ymax = max(handshake) * 1.55
    ax.set_ylim(0, ymax)
    for i, (c, curve) in enumerate(zip(ciphers, curves)):
        label = c if not curve else f"{c}\n{curve}"
        ax.text(i, handshake[i] + ymax * 0.12, label, ha="center", va="bottom", fontsize=8.5, color=INK_SECONDARY)
    ax.text(0.5, -0.24, "Note: HIGH_SECURITY negotiates AES-128-GCM here, not AES-256-GCM-SHA384 as its config lists — "
                        "Go's crypto/tls ignores CipherSuites for TLS 1.3 entirely (see Module 2 findings).",
            transform=ax.transAxes, ha="center", fontsize=8, color=INK_MUTED, wrap=True)
    savefig(fig, "03_tls_profiles_handshake.png")


# ---------------------------------------------------------------------------
# 4. Defense effectiveness (normalized % reduction)
# ---------------------------------------------------------------------------
def chart_defense_effectiveness():
    pv_rows = {r["scenario"]: r for r in read_csv(RESULTS / "property_validator" / "summary.csv")}
    ad_rows = {r["scenario"]: r for r in read_csv(RESULTS / "auth_defense" / "summary.csv")}

    pv_before = float(pv_rows["baseline_no_defense"]["broker_mem_peak_mib"])
    pv_after = float(pv_rows["with_defense"]["broker_mem_peak_mib"])
    ad_before = float(ad_rows["baseline_no_defense"]["total_sent"])
    ad_after = float(ad_rows["with_defense"]["total_sent"])

    labels = ["Property validator\n(peak broker memory)", "Auth defense\n(AUTH packets pushed through)"]
    reduction = [100 * (1 - pv_after / pv_before), 100 * (1 - ad_after / ad_before)]
    raw_before = [pv_before, ad_before]
    raw_after = [pv_after, ad_after]
    units = ["MiB", "packets"]

    fig, ax = plt.subplots(figsize=(7.5, 5))
    bars = ax.bar(labels, reduction, color=[BLUE, ORANGE], width=0.5)
    ax.set_ylabel("Reduction vs. undefended (%)")
    ax.set_ylim(0, 108)
    ax.set_title("Defense effectiveness — measured, not assumed")
    for i, b in enumerate(bars):
        ax.text(b.get_x() + b.get_width() / 2, b.get_height() + 2, f"{reduction[i]:.1f}%", ha="center", fontsize=11, fontweight="bold", color=STATUS_GOOD)
        ax.text(b.get_x() + b.get_width() / 2, b.get_height() / 2, f"{raw_before[i]:,.0f} → {raw_after[i]:,.0f} {units[i]}", ha="center", va="center", fontsize=8.5, color="white", rotation=90)
    savefig(fig, "04_defense_effectiveness.png")


# ---------------------------------------------------------------------------
# 5 & 7. Per-tier CPU overhead (message_integrity, wildcard_tokens, combined)
# ---------------------------------------------------------------------------
def chart_overhead(exp_dir, title, filename, note=None):
    base_rows = read_csv(RESULTS / exp_dir / "baseline" / "load" / "summary.csv")
    mod_rows = read_csv(RESULTS / exp_dir / "module" / "load" / "summary.csv")
    tiers = [r["tier"] for r in base_rows]
    base_cpu = [float(r["peak_broker_cpu_pct"]) for r in base_rows]
    mod_cpu = [float(r["peak_broker_cpu_pct"]) for r in mod_rows]

    x = range(len(tiers))
    width = 0.32
    fig, ax = plt.subplots(figsize=(7.5, 5))
    b1 = ax.bar([i - width / 2 for i in x], base_cpu, width, label="Baseline (no modules)", color=BLUE)
    b2 = ax.bar([i + width / 2 for i in x], mod_cpu, width, label="With module", color=ORANGE)
    ax.set_ylabel("Peak broker CPU (%)")
    ax.set_xticks(list(x))
    ax.set_xticklabels([t.replace("_", " ").title() for t in tiers])
    ax.set_title(title)
    ax.legend(frameon=False, loc="upper left")
    bar_labels(ax, b1, fmt="{:.1f}%")
    bar_labels(ax, b2, fmt="{:.1f}%")
    if note:
        ax.text(0.5, -0.22, note, transform=ax.transAxes, ha="center", fontsize=8, color=INK_MUTED, wrap=True)
    savefig(fig, filename)


# ---------------------------------------------------------------------------
# 6. Priority ordering rank distribution (new benchmark)
# ---------------------------------------------------------------------------
def chart_priority_ordering():
    order_dir = RESULTS / "priority_messaging_ordering"
    if not (order_dir / "baseline.csv").exists():
        print("skip priority_ordering: no data yet")
        return
    base_rows = read_csv(order_dir / "baseline.csv")
    mod_rows = read_csv(order_dir / "module.csv")

    base_pct = [100 * int(r["urgent_recv_rank"]) / int(r["total_received"]) for r in base_rows]
    mod_pct = [100 * int(r["urgent_recv_rank"]) / int(r["total_received"]) for r in mod_rows]

    fig, ax = plt.subplots(figsize=(8.5, 5.9))
    fig.subplots_adjust(top=0.84, bottom=0.19, left=0.13, right=0.95)
    bp = ax.boxplot(
        [base_pct, mod_pct],
        tick_labels=["Baseline (no modules)", "With priority-messaging"],
        widths=0.45,
        patch_artist=True,
        medianprops=dict(color=INK_PRIMARY, linewidth=2),
        whiskerprops=dict(color=INK_MUTED),
        capprops=dict(color=INK_MUTED),
        flierprops=dict(marker="o", markersize=4, markerfacecolor=INK_MUTED, markeredgecolor="none", alpha=0.6),
    )
    for patch, color in zip(bp["boxes"], [BLUE, ORANGE]):
        patch.set_facecolor(color)
        patch.set_alpha(0.55)
        patch.set_edgecolor(color)

    ax.set_ylabel("Urgent message's receive position (%)")
    ax.set_title("Does an Urgent message actually jump the queue?\n(lower = delivered sooner than plain send order would predict)", fontsize=12.5)
    ax.axhline(50, color=INK_MUTED, linewidth=1, linestyle="--", alpha=0.6)
    ax.text(2.55, 50, "50% = middle\nof the pack", fontsize=8, color=INK_MUTED, va="center")
    ax.set_xlim(0.4, 3.0)

    base_med = statistics.median(base_pct)
    mod_med = statistics.median(mod_pct)
    ax.text(1, base_med, f" median {base_med:.0f}%", fontsize=9, color=INK_SECONDARY, va="bottom")
    ax.text(2, mod_med, f" median {mod_med:.0f}%", fontsize=9, color=INK_SECONDARY, va="bottom")

    n = len(base_rows)
    fig.text(0.5, 0.03,
             f"Each point: one trial — a flood of Normal-priority messages from {base_rows[0].get('flood_conns', '?')} concurrent\n"
             f"connections, with one Urgent message interleaved mid-flood on one of them (n={n} trials/scenario).",
             ha="center", fontsize=8.5, color=INK_MUTED)
    savefig(fig, "05_priority_ordering_rank.png", tight=False)


# ---------------------------------------------------------------------------
# 8. QUIC before/after ALPN fix
# ---------------------------------------------------------------------------
def chart_quic_before_after():
    scenarios = ["Connect", "Reconnect", "Pub/Sub QoS0", "Pub/Sub QoS1"]
    before = [0, 0, 0, 0]
    after_rows = read_csv(RESULTS / "quic_transport" / "module" / "latency" / "latency_summary.csv")
    by_scenario = {r["scenario"]: r for r in after_rows}
    after = []
    for key in ["connect", "reconnect", "pubsub_qos0", "pubsub_qos1"]:
        r = by_scenario[key]
        after.append(100 * int(r["success_samples"]) / int(r["total_samples"]))

    x = range(len(scenarios))
    width = 0.32
    fig, ax = plt.subplots(figsize=(8, 5.2))
    b1 = ax.bar([i - width / 2 for i in x], before, width, label="Before ALPN fix", color=RED)
    b2 = ax.bar([i + width / 2 for i in x], after, width, label="After ALPN fix", color=STATUS_GOOD)
    ax.set_ylabel("Success rate (%)")
    ax.set_ylim(0, 112)
    ax.set_xticks(list(x))
    ax.set_xticklabels(scenarios)
    ax.set_title("QUIC transport: 0% → 100% after fixing the missing ALPN protocol")
    # Every bar is either 0% or 100%, so the band from ~20% to ~75% is empty
    # for all four groups — the one safe place for the legend.
    ax.legend(frameon=False, loc="center", ncols=1, bbox_to_anchor=(0.5, 0.45))
    bar_labels(ax, b1, fmt="{:.0f}%")
    bar_labels(ax, b2, fmt="{:.0f}%")
    savefig(fig, "06_quic_before_after_alpn_fix.png")


# ---------------------------------------------------------------------------
# 9. QUIC vs TCP+TLS latency
# ---------------------------------------------------------------------------
def chart_quic_vs_tcp_tls():
    base_rows = {r["scenario"]: r for r in read_csv(RESULTS / "quic_transport" / "baseline" / "latency" / "latency_summary.csv")}
    mod_rows = {r["scenario"]: r for r in read_csv(RESULTS / "quic_transport" / "module" / "latency" / "latency_summary.csv")}

    scenarios = ["connect", "reconnect", "pubsub_qos0", "pubsub_qos1"]
    labels = ["Connect", "Reconnect", "Pub/Sub QoS0", "Pub/Sub QoS1"]
    tcp_tls = [float(base_rows[s]["avg_ms"]) for s in scenarios]
    quic = [max(float(mod_rows[s]["avg_ms"]), 0) for s in scenarios]  # clip the -0.006ms measurement artifact at 0

    x = range(len(scenarios))
    width = 0.32
    fig, ax = plt.subplots(figsize=(8.5, 5.9))
    fig.subplots_adjust(top=0.9, bottom=0.24, left=0.1, right=0.96)
    b1 = ax.bar([i - width / 2 for i in x], tcp_tls, width, label="TCP + TLS", color=BLUE)
    b2 = ax.bar([i + width / 2 for i in x], quic, width, label="QUIC", color=VIOLET)
    ax.set_ylabel("Avg latency (ms)")
    ax.set_xticks(list(x))
    ax.set_xticklabels(labels)
    ax.set_title("QUIC vs TCP+TLS — real handshake cost, comparable steady-state RTT")
    ax.set_ylim(top=max(tcp_tls + quic) * 1.3)
    # Connect/Reconnect bars are tall; QoS0/QoS1 bars are near-invisible — the
    # upper area above the QoS columns is empty and safe for the legend.
    ax.legend(frameon=False, loc="upper right")
    bar_labels(ax, b1, fmt="{:.3f}")
    bar_labels(ax, b2, fmt="{:.3f}")
    fig.text(0.5, 0.03,
              "QUIC QoS1 shows 0.000ms because the measured value (-0.006ms) was clipped at zero — a clock-precision\n"
              "artifact at this sub-10-microsecond scale, not a real negative or zero latency.",
              ha="center", fontsize=8, color=INK_MUTED)
    savefig(fig, "07_quic_vs_tcp_tls_latency.png", tight=False)


# ---------------------------------------------------------------------------
# 10. eBPF self-ban by load tier
# ---------------------------------------------------------------------------
def chart_ebpf_selfban():
    rows = read_csv(RESULTS / "ebpf_filter" / "module" / "load" / "summary.csv")
    tiers = [r["tier"].replace("_", " ").title() for r in rows]
    success_pct = [100 * (int(r["workers"]) - int(r["connect_errors"])) / int(r["workers"]) for r in rows]

    fig, ax = plt.subplots(figsize=(7.5, 5))
    colors = [STATUS_GOOD if p > 50 else RED for p in success_pct]
    bars = ax.bar(tiers, success_pct, color=colors, width=0.5)
    ax.set_ylabel("Connection success rate (%)")
    ax.set_ylim(0, 108)
    ax.set_title("eBPF self-ban: the filter blocks its own load generator")
    bar_labels(ax, bars, fmt="{:.0f}%")
    ax.text(0.5, -0.2,
            "Non-root sandbox (no CAP_BPF) — app-layer fallback tracking. 127.0.0.1 trips the "
            "MaxConnectionsPerSecond=10 threshold partway through the Normal tier and stays banned for the rest of the run.",
            transform=ax.transAxes, ha="center", fontsize=8, color=INK_MUTED, wrap=True)
    savefig(fig, "08_ebpf_selfban_by_tier.png")


# ---------------------------------------------------------------------------
# 11. Combined-all-modules overhead
# ---------------------------------------------------------------------------
def main():
    chart_baseline_reference()
    chart_tls_resumption()
    chart_tls_profiles()
    chart_defense_effectiveness()
    chart_overhead(
        "message_integrity",
        "Message integrity — per-tier CPU overhead (HMAC verify on every publish)",
        "09_message_integrity_overhead.png",
    )
    chart_priority_ordering()
    chart_overhead(
        "wildcard_tokens",
        "Wildcard tokens — per-tier CPU overhead",
        "10_wildcard_tokens_overhead.png",
        note="Includes the cost of fanning out every publish to a live \"#\" subscriber (the ACL check itself is cheap; "
             "consuming a firehose subscription is the larger real cost here), not just the ACL-check hook in isolation.",
    )
    chart_quic_before_after()
    chart_quic_vs_tcp_tls()
    chart_ebpf_selfban()
    chart_overhead(
        "all_modules_combined",
        "5 modules combined (TLS resumption + adaptive profiles + message integrity\n+ priority messaging + wildcard tokens) — cumulative overhead vs baseline",
        "11_combined_all_modules_overhead.png",
        note="property-validator, auth-defense, ebpf-filter, and quic-transport are excluded from this combo — see Module notes for why.",
    )
    print("\nAll graphs written to docs/graphs/")


if __name__ == "__main__":
    main()
