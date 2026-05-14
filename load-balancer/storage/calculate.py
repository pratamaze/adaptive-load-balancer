import pandas as pd
import sys
import numpy as np
import warnings

# Matikan warning dari pandas biar output terminal bersih
warnings.filterwarnings('ignore')

def to_numeric_series(raw_series):
    cleaned = (
        raw_series.astype(str)
        .str.replace(',', '.', regex=False)
        .str.extract(r'([-+]?\d*\.?\d+)', expand=False)
    )
    return pd.to_numeric(cleaned, errors='coerce').dropna()

def evaluate_csv(file_path):
    try:
        # Baca CSV, skipinitialspace untuk mengabaikan spasi setelah koma (jika ada)
        df = pd.read_csv(file_path, skipinitialspace=True)
    except Exception as e:
        print(f"[ERROR] Gagal membaca file {file_path}: {e}")
        return

    # Pastikan data tidak kosong
    if df.empty:
        print(f"[ERROR] Dataset {file_path} kosong!")
        return

    # 1. Total Request (Karena mode per_hit, total requests = jumlah baris atau sum total_requests)
    total_requests = len(df)

    # 2. Hitung Makespan dari Timestamp UTC
    try:
        ts = pd.to_datetime(df['timestamp_utc'])
        makespan = (ts.max() - ts.min()).total_seconds()
        # Jika makespan 0 (misal cuma 1 request), jadikan 1 detik agar tidak error dibagi nol
        if makespan <= 0:
            makespan = 1.0 
    except Exception:
        makespan = 1.0

    # 3. Hitung Throughput
    throughput = total_requests / makespan

    # 4. Hitung response time GLOBAL dari semua node (tanpa filter node_id/request)
    rt_series = None
    for col in ['response_time', 'response_time_ms', 'request_latency_ms', 'latency_ms']:
        if col in df.columns:
            rt_series = to_numeric_series(df[col])
            break

    # Fallback untuk format dataset LB: gabungkan SEMUA baris dari dua kolom RT node
    if rt_series is None:
        if 'node1_response_ms' in df.columns and 'node2_response_ms' in df.columns:
            rt_series = pd.concat([
                to_numeric_series(df['node1_response_ms']),
                to_numeric_series(df['node2_response_ms'])
            ], ignore_index=True)
        else:
            print("[ERROR] Kolom response time tidak ditemukan.")
            return

    if rt_series.empty:
        print("[ERROR] Data response time kosong.")
        return

    max_response_time = float(rt_series.max())
    p99_response_time = float(np.percentile(rt_series.values, 99))

    # 5. Hitung Response Time Aktual per request terpilih (per_hit)
    # Tetap dipertahankan untuk konsistensi metrik performa lama.
    node1_req = pd.to_numeric(df['node1_requests'], errors='coerce').fillna(0).values
    node1_rt = pd.to_numeric(df['node1_response_ms'], errors='coerce').values
    node2_rt = pd.to_numeric(df['node2_response_ms'], errors='coerce').values
    actual_latency = np.where(node1_req > 0, node1_rt, node2_rt)
    actual_latency = actual_latency[~np.isnan(actual_latency)]
    if actual_latency.size > 0:
        avg_response_time = float(np.mean(actual_latency))
    else:
        avg_response_time = 0.0

    # 6. Hitung statistik CPU Aktual (Normalized)
    cpu1 = pd.to_numeric(df['node1_cpu_normalized_usage'], errors='coerce').values
    cpu2 = pd.to_numeric(df['node2_cpu_normalized_usage'], errors='coerce').values

    avg_cpu1 = float(np.nanmean(cpu1)) if np.any(~np.isnan(cpu1)) else 0.0
    avg_cpu2 = float(np.nanmean(cpu2)) if np.any(~np.isnan(cpu2)) else 0.0
    avg_system_cpu = (avg_cpu1 + avg_cpu2) / 2.0
    std_cpu1 = float(np.nanstd(cpu1, ddof=0)) if np.any(~np.isnan(cpu1)) else 0.0
    std_cpu2 = float(np.nanstd(cpu2, ddof=0)) if np.any(~np.isnan(cpu2)) else 0.0

    # 7. Hitung Keseimbangan Per Snapshot (Instantaneous DI & BCU)
    row_avg_cpu = (cpu1 + cpu2) / 2.0
    row_max_cpu = np.maximum(cpu1, cpu2)
    row_min_cpu = np.minimum(cpu1, cpu2)

    valid_row = (~np.isnan(row_avg_cpu)) & (~np.isnan(row_max_cpu)) & (~np.isnan(row_min_cpu))
    positive_avg = valid_row & (row_avg_cpu > 0)

    di_instant = np.zeros(len(df), dtype=float)
    di_instant[positive_avg] = (row_max_cpu[positive_avg] - row_min_cpu[positive_avg]) / row_avg_cpu[positive_avg]

    dev1 = np.abs(cpu1 - row_avg_cpu)
    dev2 = np.abs(cpu2 - row_avg_cpu)
    bcu_instant = np.ones(len(df), dtype=float)
    bcu_instant[positive_avg] = 1.0 - ((dev1[positive_avg] + dev2[positive_avg]) / (2.0 * row_avg_cpu[positive_avg]))
    bcu_instant = np.clip(bcu_instant, 0.0, 1.0)

    di_values = di_instant[valid_row]
    bcu_values = bcu_instant[valid_row]
    mean_di = float(np.mean(di_values)) if di_values.size > 0 else 0.0
    mean_bcu = float(np.mean(bcu_values)) if bcu_values.size > 0 else 1.0

    # ================= CETAK LAPORAN =================
    print("=======================================================")
    print(" LAPORAN EVALUASI METRIK SKRIPSI (BERBASIS POST-FLIGHT)")
    print("=======================================================")
    print(f"File Dataset Dievaluasi: {file_path}")
    print(f"Total Request Sukses   : {int(total_requests)} request")
    print("-------------------------------------------------------")
    print("METRIK KINERJA (PERFORMANCE METRICS):")
    print(f" 1. Makespan (Eq 2.4)  : {makespan:.2f} detik")
    print(f" 2. Response Time      : {avg_response_time:.2f} ms")
    print(f" 3. Throughput         : {throughput:.2f} req/detik")
    print(f" 4. Max RT (Global)    : {max_response_time:.2f} ms")
    print(f" 5. P99 RT (Global)    : {p99_response_time:.2f} ms")
    print("-------------------------------------------------------")
    print("METRIK KESEIMBANGAN (BALANCING METRICS):")
    print(f" -> Node 1 Avg CPU     : {avg_cpu1:.2f}%")
    print(f" -> Node 2 Avg CPU     : {avg_cpu2:.2f}%")
    print(f" -> Node 1 Std Dev CPU : {std_cpu1:.4f} (Semakin tinggi = semakin jungkat-jungkit)")
    print(f" -> Node 2 Std Dev CPU : {std_cpu2:.4f} (Semakin tinggi = semakin jungkat-jungkit)")
    print(f" -> Snapshot Valid     : {int(valid_row.sum())} baris")
    print(f" 6. Res. Utilization   : {avg_system_cpu:.2f}% (Rata-rata sistem)")
    print(f" 7. Mean Inst. DI      : {mean_di:.4f} (Rata-rata DI per snapshot)")
    print(f" 8. Mean Inst. BCU     : {mean_bcu:.4f} (Rata-rata BCU per snapshot)")
    print("=======================================================")

if __name__ == "__main__":
    if len(sys.argv) > 1:
        csv_file = sys.argv[1]
        evaluate_csv(csv_file)
    else:
        print("Cara penggunaan: python3 calculate.py <nama_file.csv>")
        print("Contoh         : python3 calculate.py fbase.csv")
