#!/usr/bin/env python3

import os
import sys
import argparse
import pandas as pd
import matplotlib.pyplot as plt

def parse_args():
    parser = argparse.ArgumentParser(description="Plot property validator experiment results")
    parser.add_argument("--summary-csv", required=True, help="Path to summary.csv")
    parser.add_argument("--raw-dir", required=True, help="Path to raw data directory")
    parser.add_argument("--output-dir", required=True, help="Directory to save plots")
    return parser.parse_args()

def main():
    args = parse_args()
    os.makedirs(args.output_dir, exist_ok=True)

    summary_df = pd.read_csv(args.summary_csv)
    
    # Plot CPU and Memory comparison
    fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(10, 10))
    fig.suptitle('Broker Resource Usage Under Property Injection Attack', fontsize=16)

    colors = {'baseline_no_defense': 'red', 'with_defense': 'green'}
    labels = {'baseline_no_defense': 'Baseline (No Defense)', 'with_defense': 'Property Validator Enabled'}

    for idx, row in summary_df.iterrows():
        scenario = row['scenario']
        stats_csv = row['broker_stats_csv']
        
        if os.path.exists(stats_csv):
            df = pd.read_csv(stats_csv)
            if not df.empty:
                # Convert timestamps to relative seconds
                df['timestamp'] = pd.to_datetime(df['timestamp_iso'])
                start_time = df['timestamp'].min()
                df['seconds'] = (df['timestamp'] - start_time).dt.total_seconds()
                
                # CPU Plot
                ax1.plot(df['seconds'], df['broker_cpu'], label=labels.get(scenario, scenario), color=colors.get(scenario, 'blue'), linewidth=2)
                
                # Memory Plot (KB to MB)
                ax2.plot(df['seconds'], df['broker_rss'] / 1024, label=labels.get(scenario, scenario), color=colors.get(scenario, 'blue'), linewidth=2)

    ax1.set_title('Broker CPU Usage')
    ax1.set_xlabel('Time (seconds)')
    ax1.set_ylabel('CPU Usage (%)')
    ax1.grid(True, linestyle='--', alpha=0.7)
    ax1.legend()

    ax2.set_title('Broker Memory Usage (RSS)')
    ax2.set_xlabel('Time (seconds)')
    ax2.set_ylabel('Memory (MB)')
    ax2.grid(True, linestyle='--', alpha=0.7)
    ax2.legend()

    plt.tight_layout()
    output_path = os.path.join(args.output_dir, 'cpu_memory_comparison.png')
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Generated plot: {output_path}")

    # Print summary of total packets dropped vs sent
    print("\n--- Attack Mitigation Summary ---")
    for idx, row in summary_df.iterrows():
        print(f"Scenario: {row['scenario']}")
        print(f"  Total Attempted Packets Sent: {row['total_sent']}")
        print(f"  Total Errors/Dropped: {row['total_errors']}")
        print(f"  Peak CPU: {row['broker_cpu_peak_pct']}%")
        print(f"  Peak Mem: {row['broker_mem_peak_mib']} MB\n")

if __name__ == "__main__":
    main()
