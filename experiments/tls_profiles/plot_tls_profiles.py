#!/usr/bin/env python3

import argparse
import csv
from pathlib import Path

import matplotlib.pyplot as plt


PROFILE_ORDER = {
    "LOW_POWER": 0,
    "BALANCED": 1,
    "HIGH_SECURITY": 2,
}


def to_float(value, default=0.0):
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def load_summary_rows(path: Path):
    rows = []
    with path.open("r", encoding="utf-8") as f:
        reader = csv.DictReader(f)
        for row in reader:
            if not row.get("profile"):
                continue
            rows.append(row)
    return rows


def sort_rows(rows):
    return sorted(
        rows,
        key=lambda row: (PROFILE_ORDER.get(row.get("profile", "").upper(), 99), row.get("profile", "")),
    )


def label_bars(ax, bars):
    for bar in bars:
        value = bar.get_height()
        ax.text(
            bar.get_x() + bar.get_width() / 2,
            value,
            f"{value:.2f}",
            ha="center",
            va="bottom",
            fontsize=9,
        )


def save_handshake_plot(rows, output_dir: Path):
    profiles = [row.get("profile", "") for row in rows]
    avg_vals = [to_float(row.get("handshake_avg_ms")) for row in rows]
    p95_vals = [to_float(row.get("handshake_p95_ms")) for row in rows]
    x = range(len(profiles))
    width = 0.35

    fig, ax = plt.subplots(figsize=(10, 5))
    avg_bars = ax.bar([i - width / 2 for i in x], avg_vals, width=width, label="avg_ms", color="#2A9D8F")
    p95_bars = ax.bar([i + width / 2 for i in x], p95_vals, width=width, label="p95_ms", color="#E76F51")
    ax.set_xticks(list(x))
    ax.set_xticklabels(profiles)
    ax.set_ylabel("Handshake latency (ms)")
    ax.set_title("TLS Handshake Latency by Profile")
    ax.grid(axis="y", alpha=0.25)
    ax.legend()
    label_bars(ax, avg_bars)
    label_bars(ax, p95_bars)
    fig.tight_layout()
    fig.savefig(output_dir / "handshake_latency.png", dpi=140)
    plt.close(fig)


def save_throughput_plot(rows, output_dir: Path):
    profiles = [row.get("profile", "") for row in rows]
    throughput_vals = [to_float(row.get("throughput_msgs_per_s")) for row in rows]

    fig, ax = plt.subplots(figsize=(9, 5))
    bars = ax.bar(profiles, throughput_vals, color="#1D4E89")
    ax.set_ylabel("Messages per second")
    ax.set_title("Broker Throughput by TLS Profile")
    ax.grid(axis="y", alpha=0.25)
    label_bars(ax, bars)
    fig.tight_layout()
    fig.savefig(output_dir / "throughput_msgs_per_s.png", dpi=140)
    plt.close(fig)


def save_cpu_plot(rows, output_dir: Path):
    profiles = [row.get("profile", "") for row in rows]
    avg_vals = [to_float(row.get("broker_cpu_avg_pct")) for row in rows]
    peak_vals = [to_float(row.get("broker_cpu_peak_pct")) for row in rows]
    x = range(len(profiles))
    width = 0.35

    fig, ax = plt.subplots(figsize=(10, 5))
    avg_bars = ax.bar([i - width / 2 for i in x], avg_vals, width=width, label="avg_cpu_pct", color="#3A86FF")
    peak_bars = ax.bar([i + width / 2 for i in x], peak_vals, width=width, label="peak_cpu_pct", color="#8338EC")
    ax.set_xticks(list(x))
    ax.set_xticklabels(profiles)
    ax.set_ylabel("CPU usage (%)")
    ax.set_title("Broker CPU Usage by TLS Profile")
    ax.grid(axis="y", alpha=0.25)
    ax.legend()
    label_bars(ax, avg_bars)
    label_bars(ax, peak_bars)
    fig.tight_layout()
    fig.savefig(output_dir / "broker_cpu_usage.png", dpi=140)
    plt.close(fig)


def save_memory_plot(rows, output_dir: Path):
    profiles = [row.get("profile", "") for row in rows]
    avg_vals = [to_float(row.get("broker_mem_avg_mib")) for row in rows]
    peak_vals = [to_float(row.get("broker_mem_peak_mib")) for row in rows]
    x = range(len(profiles))
    width = 0.35

    fig, ax = plt.subplots(figsize=(10, 5))
    avg_bars = ax.bar([i - width / 2 for i in x], avg_vals, width=width, label="avg_mem_mib", color="#FFB703")
    peak_bars = ax.bar([i + width / 2 for i in x], peak_vals, width=width, label="peak_mem_mib", color="#FB8500")
    ax.set_xticks(list(x))
    ax.set_xticklabels(profiles)
    ax.set_ylabel("Memory usage (MiB)")
    ax.set_title("Broker Memory Usage by TLS Profile")
    ax.grid(axis="y", alpha=0.25)
    ax.legend()
    label_bars(ax, avg_bars)
    label_bars(ax, peak_bars)
    fig.tight_layout()
    fig.savefig(output_dir / "broker_memory_usage.png", dpi=140)
    plt.close(fig)


def save_client_cpu_plot(rows, output_dir: Path):
    profiles = [row.get("profile", "") for row in rows]
    avg_vals = [to_float(row.get("client_cpu_avg_pct")) for row in rows]
    peak_vals = [to_float(row.get("client_cpu_peak_pct")) for row in rows]
    x = range(len(profiles))
    width = 0.35

    fig, ax = plt.subplots(figsize=(10, 5))
    avg_bars = ax.bar([i - width / 2 for i in x], avg_vals, width=width, label="avg_cpu_pct", color="#4361EE")
    peak_bars = ax.bar([i + width / 2 for i in x], peak_vals, width=width, label="peak_cpu_pct", color="#7209B7")
    ax.set_xticks(list(x))
    ax.set_xticklabels(profiles)
    ax.set_ylabel("CPU usage (%)")
    ax.set_title("Client CPU Usage by TLS Profile")
    ax.grid(axis="y", alpha=0.25)
    ax.legend()
    label_bars(ax, avg_bars)
    label_bars(ax, peak_bars)
    fig.tight_layout()
    fig.savefig(output_dir / "client_cpu_usage.png", dpi=140)
    plt.close(fig)


def save_client_memory_plot(rows, output_dir: Path):
    profiles = [row.get("profile", "") for row in rows]
    avg_vals = [to_float(row.get("client_mem_avg_mib")) for row in rows]
    peak_vals = [to_float(row.get("client_mem_peak_mib")) for row in rows]
    x = range(len(profiles))
    width = 0.35

    fig, ax = plt.subplots(figsize=(10, 5))
    avg_bars = ax.bar([i - width / 2 for i in x], avg_vals, width=width, label="avg_mem_mib", color="#F4A261")
    peak_bars = ax.bar([i + width / 2 for i in x], peak_vals, width=width, label="peak_mem_mib", color="#E76F51")
    ax.set_xticks(list(x))
    ax.set_xticklabels(profiles)
    ax.set_ylabel("Memory usage (MiB)")
    ax.set_title("Client Memory Usage by TLS Profile")
    ax.grid(axis="y", alpha=0.25)
    ax.legend()
    label_bars(ax, avg_bars)
    label_bars(ax, peak_bars)
    fig.tight_layout()
    fig.savefig(output_dir / "client_memory_usage.png", dpi=140)
    plt.close(fig)


def main():
    parser = argparse.ArgumentParser(description="Plot Adaptive TLS profile comparison outputs")
    parser.add_argument("--summary-csv", required=True, help="Summary CSV generated by the comparison script")
    parser.add_argument("--output-dir", required=True, help="Directory to write plot files")
    args = parser.parse_args()

    summary_csv = Path(args.summary_csv)
    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    rows = load_summary_rows(summary_csv)
    if not rows:
        raise RuntimeError(f"No rows found in summary CSV: {summary_csv}")
    rows = sort_rows(rows)

    plt.style.use("seaborn-v0_8-whitegrid")
    save_handshake_plot(rows, output_dir)
    save_throughput_plot(rows, output_dir)
    save_cpu_plot(rows, output_dir)
    save_memory_plot(rows, output_dir)
    save_client_cpu_plot(rows, output_dir)
    save_client_memory_plot(rows, output_dir)

    with (output_dir / "plots_manifest.csv").open("w", encoding="utf-8", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["plot_file"])
        writer.writerow(["handshake_latency.png"])
        writer.writerow(["throughput_msgs_per_s.png"])
        writer.writerow(["broker_cpu_usage.png"])
        writer.writerow(["broker_memory_usage.png"])
        writer.writerow(["client_cpu_usage.png"])
        writer.writerow(["client_memory_usage.png"])


if __name__ == "__main__":
    main()
