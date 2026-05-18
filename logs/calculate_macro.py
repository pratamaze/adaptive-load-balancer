import sys

import numpy as np
import pandas as pd

try:
    from calculate import build_effective_requests, compute_macro_di_bcu, resolve_cpu_columns
except ImportError:
    from logs.calculate import build_effective_requests, compute_macro_di_bcu, resolve_cpu_columns


def evaluate_csv(file_path):
    try:
        df = pd.read_csv(file_path, skipinitialspace=True)
    except Exception as exc:
        print("[ERROR] Gagal membaca file {}: {}".format(file_path, exc))
        return

    if df.empty:
        print("[ERROR] Dataset {} kosong!".format(file_path))
        return

    _, _, _, effective_requests, active_traffic_mask, _, _ = build_effective_requests(df)
    if not np.any(active_traffic_mask):
        print("[ERROR] Dataset {} tidak memiliki snapshot traffic aktif (total_requests > 0 atau node1_requests + node2_requests > 0).".format(file_path))
        return

    node1_cpu_col, node2_cpu_col = resolve_cpu_columns(df)
    if node1_cpu_col is None or node2_cpu_col is None:
        print("[ERROR] Kolom CPU normalized tidak ditemukan.")
        return

    cpu1 = pd.to_numeric(df[node1_cpu_col], errors='coerce').to_numpy(dtype=float)
    cpu2 = pd.to_numeric(df[node2_cpu_col], errors='coerce').to_numpy(dtype=float)

    cpu1_active = cpu1[active_traffic_mask & ~np.isnan(cpu1)]
    cpu2_active = cpu2[active_traffic_mask & ~np.isnan(cpu2)]
    if cpu1_active.size == 0 or cpu2_active.size == 0:
        print("[ERROR] Kolom CPU normalized kosong pada snapshot traffic aktif.")
        return

    avg_cpu_1 = float(np.mean(cpu1_active))
    avg_cpu_2 = float(np.mean(cpu2_active))
    avg_cpu_system = (avg_cpu_1 + avg_cpu_2) / 2.0
    macro_di, macro_bcu = compute_macro_di_bcu(avg_cpu_1, avg_cpu_2)

    print("File: {}".format(file_path))
    print("Snapshot aktif: {}".format(int(active_traffic_mask.sum())))
    print("Total request aktif: {:.0f}".format(float(np.sum(effective_requests[active_traffic_mask]))))
    print("AvgCPU_1: {:.6f}".format(avg_cpu_1))
    print("AvgCPU_2: {:.6f}".format(avg_cpu_2))
    print("AvgCPU_system: {:.6f}".format(avg_cpu_system))
    print("DI_macro: {:.6f} (0 terbaik, 1 terburuk; |AvgCPU1-AvgCPU2|/(AvgCPU1+AvgCPU2))".format(macro_di))
    print("BCU_macro: {:.6f} (1 terbaik, 0 terburuk; 1-DI_macro)".format(macro_bcu))


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Cara penggunaan: python3 calculate_macro.py <nama_file.csv>")
        sys.exit(1)
    evaluate_csv(sys.argv[1])
