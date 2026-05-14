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

def weighted_percentile(values, weights, percentile):
    """Hitung persentil berbobot tanpa mengulang array (kompatibel numpy lama)."""
    if values.size == 0 or weights.size == 0:
        return 0.0
    total_weight = np.sum(weights)
    if total_weight <= 0:
        return 0.0

    sorted_idx = np.argsort(values)
    sorted_values = values[sorted_idx]
    sorted_weights = weights[sorted_idx]
    cumulative = np.cumsum(sorted_weights)
    threshold = (percentile / 100.0) * total_weight
    pos = np.searchsorted(cumulative, threshold, side='left')
    pos = min(pos, len(sorted_values) - 1)
    return float(sorted_values[pos])

def evaluate_csv(file_path):
    try:
        # Baca CSV, skipinitialspace untuk mengabaikan spasi setelah koma (jika ada)
        df = pd.read_csv(file_path, skipinitialspace=True)
    except Exception as e:
        print("[ERROR] Gagal membaca file {}: {}".format(file_path, e))
        return

    # Pastikan data tidak kosong
    if df.empty:
        print("[ERROR] Dataset {} kosong!".format(file_path))
        return

    node1_req = pd.to_numeric(df.get('node1_requests'), errors='coerce').fillna(0).values
    node2_req = pd.to_numeric(df.get('node2_requests'), errors='coerce').fillna(0).values
    node1_req = np.where(node1_req < 0, 0, node1_req)
    node2_req = np.where(node2_req < 0, 0, node2_req)

    # 1. Total Request
    # Dataset per_hit di proyek ini menyimpan request agregat per row.
    # Jadi total request harus dihitung dari sum total_requests / node requests, bukan jumlah baris.
    total_req_col = pd.to_numeric(df.get('total_requests'), errors='coerce').fillna(0).values
    total_req_col = np.where(total_req_col < 0, 0, total_req_col)

    summed_total_req_col = float(np.nansum(total_req_col))
    summed_node_req = float(np.nansum(node1_req + node2_req))

    if summed_total_req_col > 0:
        total_requests = summed_total_req_col
    elif summed_node_req > 0:
        total_requests = summed_node_req
    else:
        total_requests = float(len(df))

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

    # 4-5. Hitung response time (weighted per request agar relevan untuk row agregat).
    node1_rt = pd.to_numeric(df.get('node1_response_ms'), errors='coerce').values
    node2_rt = pd.to_numeric(df.get('node2_response_ms'), errors='coerce').values

    has_node_rt = ('node1_response_ms' in df.columns) and ('node2_response_ms' in df.columns)
    has_node_req = ('node1_requests' in df.columns) and ('node2_requests' in df.columns)

    weighted_rt_values = np.array([], dtype=float)
    weighted_rt_counts = np.array([], dtype=float)

    if has_node_rt and has_node_req:
        valid1 = (node1_req > 0) & (~np.isnan(node1_rt))
        valid2 = (node2_req > 0) & (~np.isnan(node2_rt))

        rt_values_parts = []
        rt_counts_parts = []
        if np.any(valid1):
            rt_values_parts.append(node1_rt[valid1])
            rt_counts_parts.append(node1_req[valid1])
        if np.any(valid2):
            rt_values_parts.append(node2_rt[valid2])
            rt_counts_parts.append(node2_req[valid2])

        if rt_values_parts:
            weighted_rt_values = np.concatenate(rt_values_parts)
            weighted_rt_counts = np.concatenate(rt_counts_parts)

    # Fallback generic single-latency-column (diasumsikan per-row mewakili 1 request)
    if weighted_rt_values.size == 0:
        rt_series = None
        for col in ['response_time', 'response_time_ms', 'request_latency_ms', 'latency_ms']:
            if col in df.columns:
                rt_series = to_numeric_series(df[col])
                break
        if rt_series is None or rt_series.empty:
            print("[ERROR] Kolom response time tidak ditemukan atau kosong.")
            return

        weighted_rt_values = rt_series.values.astype(float)
        weighted_rt_counts = np.ones_like(weighted_rt_values, dtype=float)

    total_rt_weight = float(np.sum(weighted_rt_counts))
    if total_rt_weight > 0:
        avg_response_time = float(np.sum(weighted_rt_values * weighted_rt_counts) / total_rt_weight)
    else:
        avg_response_time = 0.0

    max_response_time = float(np.max(weighted_rt_values)) if weighted_rt_values.size > 0 else 0.0
    p99_response_time = weighted_percentile(weighted_rt_values, weighted_rt_counts, 99.0)

    # 6. Hitung statistik CPU Aktual (Normalized)
    if 'node1_cpu_normalized_usage' not in df.columns or 'node2_cpu_normalized_usage' not in df.columns:
        print("[ERROR] Kolom CPU normalized tidak ditemukan.")
        return
    cpu1 = pd.to_numeric(df['node1_cpu_normalized_usage'], errors='coerce').values
    cpu2 = pd.to_numeric(df['node2_cpu_normalized_usage'], errors='coerce').values

    avg_cpu1 = float(np.nanmean(cpu1)) if np.any(~np.isnan(cpu1)) else 0.0
    avg_cpu2 = float(np.nanmean(cpu2)) if np.any(~np.isnan(cpu2)) else 0.0
    avg_system_cpu = (avg_cpu1 + avg_cpu2) / 2.0
    std_cpu1 = float(np.nanstd(cpu1, ddof=0)) if np.any(~np.isnan(cpu1)) else 0.0
    std_cpu2 = float(np.nanstd(cpu2, ddof=0)) if np.any(~np.isnan(cpu2)) else 0.0

    # 7. Hitung Keseimbangan Per Snapshot (Instantaneous DI & BCU)
    row_avg_cpu = (cpu1 + cpu2) / 2.0

    valid_row = (~np.isnan(row_avg_cpu)) & (~np.isnan(cpu1)) & (~np.isnan(cpu2))
    positive_avg = valid_row & (row_avg_cpu > 0)

    di_instant = np.zeros(len(df), dtype=float)
    di_instant[positive_avg] = np.abs(cpu1[positive_avg] - cpu2[positive_avg]) / row_avg_cpu[positive_avg]

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
    print("File Dataset Dievaluasi: {}".format(file_path))
    print("Total Request Sukses   : {} request".format(int(total_requests)))
    print("-------------------------------------------------------")
    print("METRIK KINERJA (PERFORMANCE METRICS):")
    print(" 1. Makespan (Eq 2.4)  : {:.2f} detik".format(makespan))
    print(" 2. Response Time      : {:.2f} ms".format(avg_response_time))
    print(" 3. Throughput         : {:.2f} req/detik".format(throughput))
    print(" 4. Max RT (Global)    : {:.2f} ms".format(max_response_time))
    print(" 5. P99 RT (Global)    : {:.2f} ms".format(p99_response_time))
    print("-------------------------------------------------------")
    print("METRIK KESEIMBANGAN (BALANCING METRICS):")
    print(" -> Node 1 Avg CPU     : {:.2f}%".format(avg_cpu1))
    print(" -> Node 2 Avg CPU     : {:.2f}%".format(avg_cpu2))
    print(" -> Node 1 Std Dev CPU : {:.4f} (Semakin tinggi = semakin jungkat-jungkit)".format(std_cpu1))
    print(" -> Node 2 Std Dev CPU : {:.4f} (Semakin tinggi = semakin jungkat-jungkit)".format(std_cpu2))
    print(" -> Snapshot Valid     : {} baris".format(int(valid_row.sum())))
    print(" 6. Res. Utilization   : {:.2f}% (Rata-rata sistem)".format(avg_system_cpu))
    print(" 7. Mean Inst. DI      : {:.4f} (|CPU1-CPU2|/CPUavg per snapshot)".format(mean_di))
    print(" 8. Mean Inst. BCU     : {:.4f} (Rata-rata BCU per snapshot)".format(mean_bcu))
    print("=======================================================")

if __name__ == "__main__":
    if len(sys.argv) > 1:
        csv_file = sys.argv[1]
        evaluate_csv(csv_file)
    else:
        print("Cara penggunaan: python3 calculate.py <nama_file.csv>")
        print("Contoh         : python3 calculate.py fbase.csv")
