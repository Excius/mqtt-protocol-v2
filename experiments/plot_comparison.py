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

def to_float(v, default=0.0):
    try:
        return float(v)
    except (TypeError, ValueError):
        return default

def cdf_points(values):
    if not values:
        return [], []
    values = sorted(values)
    n = len(values)
    xs = values
    ys = [(i + 1) / n for i in range(n)]
    return xs, ys

def extract_latencies(rows):
    vals = []
    for r in rows:
        if r.get("success", "").lower() != "true":
            continue
        raw = r.get("latency_ms")
        if raw in (None, ""):
            continue
        vals.append(to_float(raw))
    return vals

def extract_cpu(rows):
    vals = []
    for r in rows:
        raw = r.get("broker_cpu_pct")
        if raw in (None, ""):
            continue
        vals.append(to_float(raw))
    if not vals:
        return 0.0
    return sum(vals)/len(vals)

def label_bars(ax, bars):
    for bar in bars:
        val = bar.get_height()
        ax.text(bar.get_x() + bar.get_width() / 2, val, f"{val:.2f}", ha="center", va="bottom", fontsize=8)


def main():
    parser = argparse.ArgumentParser(description="Plot comparison between baseline and module")
    parser.add_argument("--baseline-dir", required=True, help="Baseline results directory")
    parser.add_argument("--module-dir", required=True, help="Module results directory")
    parser.add_argument("--module-name", required=True, help="Name of the module for the legend")
    parser.add_argument("--output-dir", required=True, help="Plot output directory")
    args = parser.parse_args()

    baseline_dir = Path(args.baseline_dir)
    module_dir = Path(args.module_dir)
    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    b_lat_rows = load_csv(baseline_dir / "latency" / "raw" / "latency.csv")
    m_lat_rows = load_csv(module_dir / "latency" / "raw" / "latency.csv")
    
    b_latencies = extract_latencies(b_lat_rows)
    m_latencies = extract_latencies(m_lat_rows)

    plt.style.use("seaborn-v0_8-whitegrid")
    
    # 1. Latency CDF Plot
    plt.figure(figsize=(10, 5))
    plotted = False
    
    xs, ys = cdf_points(b_latencies)
    if xs:
        plt.plot(xs, ys, linewidth=2, label="Baseline")
        plotted = True
        
    xs, ys = cdf_points(m_latencies)
    if xs:
        plt.plot(xs, ys, linewidth=2, label=args.module_name)
        plotted = True

    plt.xlabel("Latency (ms)")
    plt.ylabel("CDF")
    plt.title(f"Latency CDF: Baseline vs {args.module_name}")
    plt.grid(alpha=0.3)
    if plotted:
        plt.legend()
    plt.tight_layout()
    plt.savefig(output_dir / "latency_comparison.png", dpi=140)
    plt.close()

    # 2. CPU / Resource Bar Chart
    b_load_rows = load_csv(baseline_dir / "latency" / "raw" / "load_30s.csv")
    m_load_rows = load_csv(module_dir / "latency" / "raw" / "load_30s.csv")

    b_cpu = extract_cpu(b_load_rows)
    m_cpu = extract_cpu(m_load_rows)

    fig, ax = plt.subplots(figsize=(6, 5))
    bars = ax.bar(["Baseline", args.module_name], [b_cpu, m_cpu], color=["#3A86FF", "#FF006E"])
    ax.set_title("Broker Average CPU (%)")
    ax.set_ylabel("%")
    ax.grid(axis="y", alpha=0.25)
    label_bars(ax, bars)
    
    fig.tight_layout()
    fig.savefig(output_dir / "cpu_comparison.png", dpi=140)
    plt.close(fig)
    
    print(f"Plots saved to {output_dir}")


if __name__ == "__main__":
    main()
