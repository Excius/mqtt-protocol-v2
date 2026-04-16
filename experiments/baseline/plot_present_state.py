#!/usr/bin/env python3

import argparse
import csv
from pathlib import Path

import matplotlib.pyplot as plt


def load_csv(path: Path):
    rows = []
    if not path.exists():
        return rows
    with path.open("r", encoding="utf-8") as f:
        reader = csv.DictReader(f)
        for row in reader:
            rows.append(row)
    return rows


def require_rows(path: Path, label: str):
    rows = load_csv(path)
    if not rows:
        raise RuntimeError(f"Missing or empty CSV for {label}: {path}")
    return rows


def to_float(v, default=0.0):
    try:
        return float(v)
    except (TypeError, ValueError):
        return default


def extract_metric(rows, key):
    vals = []
    for r in rows:
        if r.get("success", "").lower() != "true":
            continue
        raw = r.get(key)
        if raw in (None, ""):
            continue
        vals.append(to_float(raw))
    return vals


def cdf_points(values):
    if not values:
        return [], []
    values = sorted(values)
    n = len(values)
    xs = values
    ys = [(i + 1) / n for i in range(n)]
    return xs, ys


def percentile(values, p):
    if not values:
        return 0.0
    values = sorted(values)
    idx = int((p / 100.0) * (len(values) - 1))
    idx = min(max(idx, 0), len(values) - 1)
    return values[idx]


def save_latency_cdf(series, out_path):
    plt.figure(figsize=(10, 5))
    plotted = False
    for name, values in series.items():
        xs, ys = cdf_points(values)
        if xs:
            plt.plot(xs, ys, linewidth=2, label=name)
            plotted = True

    plt.xlabel("Latency (ms)")
    plt.ylabel("CDF")
    plt.title("Latency CDF")
    plt.grid(alpha=0.3)
    if plotted:
        plt.legend()
    plt.tight_layout()
    plt.savefig(out_path, dpi=140)
    plt.close()


def save_latency_hist(series, out_path):
    fig, axes = plt.subplots(2, 2, figsize=(12, 8))
    keys = list(series.keys())
    for i, ax in enumerate(axes.flat):
        if i >= len(keys):
            ax.axis("off")
            continue
        name = keys[i]
        vals = series[name]
        if vals:
            ax.hist(vals, bins=40, color="#00798C", alpha=0.85)
        ax.set_title(name)
        ax.set_xlabel("Latency (ms)")
        ax.set_ylabel("Count")
        ax.grid(alpha=0.25)

    fig.suptitle("Latency Distributions")
    fig.tight_layout()
    fig.savefig(out_path, dpi=140)
    plt.close(fig)


def save_percentile_bar(series, out_path):
    labels = []
    p50 = []
    p95 = []
    p99 = []

    for name, vals in series.items():
        labels.append(name)
        p50.append(percentile(vals, 50))
        p95.append(percentile(vals, 95))
        p99.append(percentile(vals, 99))

    x = range(len(labels))

    fig, ax = plt.subplots(figsize=(10, 5))
    width = 0.25
    ax.bar([v - width for v in x], p50, width=width, label="p50", color="#30638E")
    ax.bar(list(x), p95, width=width, label="p95", color="#D1495B")
    ax.bar([v + width for v in x], p99, width=width, label="p99", color="#EDAe49")

    ax.set_xticks(list(x))
    ax.set_xticklabels(labels, rotation=20, ha="right")
    ax.set_ylabel("Latency (ms)")
    ax.set_title("Latency Percentiles")
    ax.grid(axis="y", alpha=0.25)
    ax.legend()

    fig.tight_layout()
    fig.savefig(out_path, dpi=140)
    plt.close(fig)


def save_idle_trend(idle_rows, out_path):
    xs = [to_float(r.get("elapsed_s")) for r in idle_rows]
    cpu = [to_float(r.get("broker_cpu_pct")) for r in idle_rows]
    rss = [to_float(r.get("broker_rss_kb")) / 1024.0 for r in idle_rows]

    fig, ax1 = plt.subplots(figsize=(10, 4.8))
    ax1.plot(xs, cpu, color="#D1495B", linewidth=2, label="Broker CPU %")
    ax1.set_xlabel("Elapsed seconds")
    ax1.set_ylabel("CPU %", color="#D1495B")
    ax1.tick_params(axis="y", labelcolor="#D1495B")
    ax1.grid(alpha=0.25)

    ax2 = ax1.twinx()
    ax2.plot(xs, rss, color="#00798C", linewidth=2, label="Broker RSS MiB")
    ax2.set_ylabel("RSS (MiB)", color="#00798C")
    ax2.tick_params(axis="y", labelcolor="#00798C")

    fig.suptitle("Idle Broker Resource Trend")
    fig.tight_layout()
    fig.savefig(out_path, dpi=140)
    plt.close(fig)


def save_percentile_csv(series, out_path):
    with out_path.open("w", encoding="utf-8", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["scenario", "count", "avg_ms", "p50_ms", "p95_ms", "p99_ms"])
        for name, vals in series.items():
            if vals:
                avg = sum(vals) / len(vals)
            else:
                avg = 0.0
            writer.writerow([
                name,
                len(vals),
                f"{avg:.3f}",
                f"{percentile(vals, 50):.3f}",
                f"{percentile(vals, 95):.3f}",
                f"{percentile(vals, 99):.3f}",
            ])


def save_load_tier_overview(summary_rows, out_path):
    tiers = []
    peak_cpu = []
    avg_rss_mib = []
    avg_net_mib = []

    for row in summary_rows:
        tiers.append(row.get("tier", ""))
        peak_cpu.append(to_float(row.get("peak_broker_cpu_pct")))
        avg_rss_mib.append(to_float(row.get("avg_broker_rss_kb")) / 1024.0)
        rx = to_float(row.get("avg_net_rx_bytes_per_sample"))
        tx = to_float(row.get("avg_net_tx_bytes_per_sample"))
        avg_net_mib.append((rx + tx) / (1024.0 * 1024.0))

    fig, axes = plt.subplots(1, 3, figsize=(12, 4.8))
    color = "#00798C"

    axes[0].bar(tiers, peak_cpu, color=color)
    axes[0].set_title("Peak Broker CPU (%)")
    axes[0].set_ylabel("CPU %")
    axes[0].grid(axis="y", alpha=0.25)

    axes[1].bar(tiers, avg_rss_mib, color="#30638E")
    axes[1].set_title("Avg Broker RSS (MiB)")
    axes[1].set_ylabel("MiB")
    axes[1].grid(axis="y", alpha=0.25)

    axes[2].bar(tiers, avg_net_mib, color="#EDAe49")
    axes[2].set_title("Avg Net RX+TX / Sample (MiB)")
    axes[2].set_ylabel("MiB")
    axes[2].grid(axis="y", alpha=0.25)

    for ax in axes:
        ax.tick_params(axis="x", rotation=20)

    fig.suptitle("Load Tier Overview")
    fig.tight_layout()
    fig.savefig(out_path, dpi=140)
    plt.close(fig)


def save_load_cpu_rss_trend(series_by_tier, out_path):
    fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(11, 8), sharex=False)

    palette = {
        "normal": "#30638E",
        "high": "#D1495B",
        "very_high": "#00798C",
    }

    for tier, rows in series_by_tier.items():
        xs = [to_float(r.get("elapsed_s")) for r in rows]
        cpu = [to_float(r.get("broker_cpu_pct")) for r in rows]
        rss = [to_float(r.get("broker_rss_kb")) / 1024.0 for r in rows]
        color = palette.get(tier, None)

        ax1.plot(xs, cpu, linewidth=2, label=tier, color=color)
        ax2.plot(xs, rss, linewidth=2, label=tier, color=color)

    ax1.set_title("Load Tier Broker CPU Trend")
    ax1.set_ylabel("CPU %")
    ax1.grid(alpha=0.25)
    ax1.legend()

    ax2.set_title("Load Tier Broker RSS Trend")
    ax2.set_xlabel("Elapsed seconds")
    ax2.set_ylabel("RSS (MiB)")
    ax2.grid(alpha=0.25)
    ax2.legend()

    fig.tight_layout()
    fig.savefig(out_path, dpi=140)
    plt.close(fig)


def main():
    parser = argparse.ArgumentParser(description="Plot present-state latency and idle metrics")
    parser.add_argument("--latency-dir", required=True, help="Latency directory containing raw and summary CSV files")
    parser.add_argument("--output-dir", required=True, help="Directory to write latency plots")
    parser.add_argument("--load-dir", help="Load directory containing summary.csv and raw tier CSV files")
    parser.add_argument("--load-output-dir", help="Directory to write load plots")
    args = parser.parse_args()

    latency_dir = Path(args.latency_dir)
    output_dir = Path(args.output_dir)
    raw_dir = latency_dir / "raw"
    output_dir.mkdir(parents=True, exist_ok=True)

    load_dir = Path(args.load_dir) if args.load_dir else None
    load_output_dir = Path(args.load_output_dir) if args.load_output_dir else None
    if load_dir and not load_output_dir:
        raise RuntimeError("--load-output-dir is required when --load-dir is set")
    if load_output_dir:
        load_output_dir.mkdir(parents=True, exist_ok=True)

    plt.style.use("seaborn-v0_8-whitegrid")

    connect_rows = require_rows(raw_dir / "connect_latency.csv", "connect latency")
    reconnect_rows = require_rows(raw_dir / "reconnect_latency.csv", "reconnect latency")
    pubsub0_rows = require_rows(raw_dir / "pubsub_qos0_rtt.csv", "pubsub qos0")
    pubsub1_rows = require_rows(raw_dir / "pubsub_qos1_rtt.csv", "pubsub qos1")
    idle_rows = require_rows(raw_dir / "idle_30s.csv", "idle baseline")

    series = {
        "connect": extract_metric(connect_rows, "connect_ms"),
        "reconnect": extract_metric(reconnect_rows, "reconnect_ms"),
        "pubsub_qos0": extract_metric(pubsub0_rows, "rtt_ms"),
        "pubsub_qos1": extract_metric(pubsub1_rows, "rtt_ms"),
    }

    for name, values in series.items():
        if not values:
            raise RuntimeError(f"No successful latency samples for {name}")

    save_latency_cdf(series, output_dir / "latency_cdf.png")
    save_latency_hist(series, output_dir / "latency_histograms.png")
    save_percentile_bar(series, output_dir / "latency_percentiles.png")
    save_percentile_csv(series, output_dir / "latency_percentiles.csv")

    save_idle_trend(idle_rows, output_dir / "idle_resource_trend.png")

    with (output_dir / "plots_manifest.csv").open("w", encoding="utf-8", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["plot_file"])
        writer.writerow(["latency_cdf.png"])
        writer.writerow(["latency_histograms.png"])
        writer.writerow(["latency_percentiles.png"])
        writer.writerow(["latency_percentiles.csv"])
        writer.writerow(["idle_resource_trend.png"])

    if load_dir and load_output_dir:
        summary_rows = require_rows(load_dir / "summary.csv", "load summary")
        normal_rows = require_rows(load_dir / "raw" / "normal.csv", "normal tier")
        high_rows = require_rows(load_dir / "raw" / "high.csv", "high tier")
        very_high_rows = require_rows(load_dir / "raw" / "very_high.csv", "very high tier")

        save_load_tier_overview(summary_rows, load_output_dir / "load_tier_overview.png")
        save_load_cpu_rss_trend(
            {
                "normal": normal_rows,
                "high": high_rows,
                "very_high": very_high_rows,
            },
            load_output_dir / "load_cpu_rss_trend.png",
        )

        with (load_output_dir / "load_plots_manifest.csv").open("w", encoding="utf-8", newline="") as f:
            writer = csv.writer(f)
            writer.writerow(["plot_file"])
            writer.writerow(["load_tier_overview.png"])
            writer.writerow(["load_cpu_rss_trend.png"])


if __name__ == "__main__":
    main()
