import pandas as pd
import sys
import numpy as np
import warnings

# Matikan warning dari pandas biar output terminal bersih
warnings.filterwarnings('ignore')

NORMALIZED_CPU_CAPACITY = 100.0

def find_column(lower_to_original, aliases):
    for alias in aliases:
        key = alias.strip().lower()
        if key in lower_to_original:
            return lower_to_original[key]
    return None

def read_non_negative_column(df, aliases):
    lower_to_original = {name.strip().lower(): name for name in df.columns}
    column_name = find_column(lower_to_original, aliases)
    if column_name is None:
        return np.zeros(len(df), dtype=float), None

    values = pd.to_numeric(df[column_name], errors='coerce').fillna(0).to_numpy(dtype=float)
    values = np.where(values < 0, 0, values)
    return values, column_name

def build_effective_requests(df):
    total_req_col, total_req_name = read_non_negative_column(df, ['total_requests'])
    node1_req, node1_req_name = read_non_negative_column(df, ['node1_requests'])
    node2_req, node2_req_name = read_non_negative_column(df, ['node2_requests'])

    # Samakan dengan notebook Colab: request node dibulatkan ke count terdekat.
    node1_req = np.rint(node1_req).astype(float)
    node2_req = np.rint(node2_req).astype(float)

    has_total_req = total_req_name is not None
    has_node_req = node1_req_name is not None and node2_req_name is not None
    summed_node_req = node1_req + node2_req

    if has_total_req and has_node_req:
        # Notebook memakai max(total_requests, node1_requests + node2_requests).
        effective_requests = np.maximum(total_req_col, summed_node_req)
    elif has_total_req:
        effective_requests = total_req_col
    elif has_node_req:
        effective_requests = summed_node_req
    else:
        effective_requests = np.ones(len(df), dtype=float)

    effective_requests = np.where(effective_requests < 0, 0, effective_requests)
    active_traffic_mask = effective_requests > 0
    return node1_req, node2_req, total_req_col, effective_requests, active_traffic_mask, has_total_req, has_node_req

def resolve_cpu_columns(df):
    lower_to_original = {name.strip().lower(): name for name in df.columns}
    node1_col = find_column(lower_to_original, [
        'node1_cpu_normalized_usage',
        'node1_cpu_usage_normalized',
        'node1_cpu_normalized',
        'node_1_cpu_normalized_usage',
    ])
    node2_col = find_column(lower_to_original, [
        'node2_cpu_normalized_usage',
        'node2_cpu_usage_normalized',
        'node2_cpu_normalized',
        'node_2_cpu_normalized_usage',
    ])
    return node1_col, node2_col

def resolve_raw_cpu_columns(df):
    lower_to_original = {name.strip().lower(): name for name in df.columns}
    node1_col = find_column(lower_to_original, [
        'node1_cpu_raw_usage',
        'node1_cpu_usage_raw',
    ])
    node2_col = find_column(lower_to_original, [
        'node2_cpu_raw_usage',
        'node2_cpu_usage_raw',
    ])
    return node1_col, node2_col

def clean_response_series_like_colab(values, fallback=1.0):
    """
    Samakan perilaku notebook Colab:
    - nilai <= 0 dianggap missing
    - isi dengan ffill/bfill
    - fallback terakhir ke angka default
    """
    numeric = pd.to_numeric(pd.Series(values), errors='coerce').astype('float64')
    numeric = numeric.replace([np.inf, -np.inf], np.nan)
    numeric = numeric.mask(numeric <= 0)

    finite = numeric.dropna()
    fallback_value = float(finite.iloc[0]) if not finite.empty else float(fallback)
    numeric = numeric.ffill().bfill().fillna(fallback_value)
    return numeric.to_numpy(dtype=float)

def compute_di_bcu_series(cpu1, cpu2):
    valid = (~np.isnan(cpu1)) & (~np.isnan(cpu2))

    di_values = np.zeros(len(cpu1), dtype=float)
    di_values[valid] = np.abs(cpu1[valid] - cpu2[valid]) / NORMALIZED_CPU_CAPACITY
    di_values = np.clip(di_values, 0.0, 1.0)

    bcu_values = np.ones(len(cpu1), dtype=float)
    bcu_values[valid] = 1.0 - di_values[valid]
    bcu_values = np.clip(bcu_values, 0.0, 1.0)
    return valid, di_values, bcu_values

def compute_macro_di_bcu(avg_cpu1, avg_cpu2):
    macro_di = abs(avg_cpu1 - avg_cpu2) / NORMALIZED_CPU_CAPACITY
    macro_di = float(np.clip(macro_di, 0.0, 1.0))
    macro_bcu = float(np.clip(1.0 - macro_di, 0.0, 1.0))
    return macro_di, macro_bcu

def count_positive_request_zero_cpu_rows(node_req, cpu_values):
    return int(np.sum((node_req > 0) & (cpu_values <= 0)))

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

    node1_req, node2_req, total_req_col, effective_requests, active_traffic_mask, has_total_req, has_node_req = build_effective_requests(df)
    total_requests = float(np.nansum(effective_requests))

    if not np.any(active_traffic_mask):
        print("[ERROR] Dataset {} tidak memiliki snapshot traffic aktif (total_requests > 0 atau node1_requests + node2_requests > 0).".format(file_path))
        return

    # 2. Hitung Makespan dari Timestamp UTC
    try:
        ts = pd.to_datetime(df.loc[active_traffic_mask, 'timestamp_utc'], errors='coerce').dropna()
        if ts.empty:
            raise ValueError("timestamp_utc kosong")
        makespan = (ts.max() - ts.min()).total_seconds()
        # Jika makespan 0 (misal cuma 1 request), jadikan 1 detik agar tidak error dibagi nol
        if makespan <= 0:
            makespan = 1.0
    except Exception:
        makespan = 1.0

    # 3. Hitung Throughput
    throughput = total_requests / makespan

    # 4-5. Hitung response time (weighted per request agar relevan untuk row agregat).
    node1_rt_raw = pd.to_numeric(df['node1_response_ms'], errors='coerce').to_numpy(dtype=float) if 'node1_response_ms' in df.columns else np.full(len(df), np.nan)
    node2_rt_raw = pd.to_numeric(df['node2_response_ms'], errors='coerce').to_numpy(dtype=float) if 'node2_response_ms' in df.columns else np.full(len(df), np.nan)

    has_node_rt = ('node1_response_ms' in df.columns) and ('node2_response_ms' in df.columns)
    node1_rt = clean_response_series_like_colab(node1_rt_raw, fallback=1.0) if has_node_rt else node1_rt_raw
    node2_rt = clean_response_series_like_colab(node2_rt_raw, fallback=1.0) if has_node_rt else node2_rt_raw

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

    # Fallback generic single-latency-column.
    # Jika ada kolom request, bobotnya mengikuti effective_requests per row.
    if weighted_rt_values.size == 0:
        for col in ['response_time', 'response_time_ms', 'request_latency_ms', 'latency_ms']:
            if col in df.columns:
                rt_values_all = pd.to_numeric(df[col], errors='coerce').to_numpy(dtype=float)
                valid_rt = ~np.isnan(rt_values_all)
                if has_total_req or has_node_req:
                    valid_rt = valid_rt & active_traffic_mask
                if not np.any(valid_rt):
                    continue

                weighted_rt_values = rt_values_all[valid_rt].astype(float)
                if has_total_req or has_node_req:
                    weighted_rt_counts = effective_requests[valid_rt].astype(float)
                else:
                    weighted_rt_counts = np.ones_like(weighted_rt_values, dtype=float)
                break

        if weighted_rt_values.size == 0:
            print("[ERROR] Kolom response time tidak ditemukan atau kosong.")
            return

    total_rt_weight = float(np.sum(weighted_rt_counts))
    if total_rt_weight > 0:
        avg_response_time = float(np.sum(weighted_rt_values * weighted_rt_counts) / total_rt_weight)
    else:
        avg_response_time = 0.0

    max_response_time = float(np.max(weighted_rt_values)) if weighted_rt_values.size > 0 else 0.0
    p99_response_time = weighted_percentile(weighted_rt_values, weighted_rt_counts, 99.0)

    # 6. Hitung statistik CPU Aktual (Normalized) hanya pada snapshot traffic aktif.
    node1_cpu_col, node2_cpu_col = resolve_cpu_columns(df)
    if node1_cpu_col is None or node2_cpu_col is None:
        print("[ERROR] Kolom CPU normalized tidak ditemukan.")
        return

    cpu1 = (
        pd.to_numeric(df[node1_cpu_col], errors='coerce')
        .replace([np.inf, -np.inf], np.nan)
        .fillna(0.0)
        .clip(lower=0.0, upper=100.0)
        .to_numpy(dtype=float)
    )
    cpu2 = (
        pd.to_numeric(df[node2_cpu_col], errors='coerce')
        .replace([np.inf, -np.inf], np.nan)
        .fillna(0.0)
        .clip(lower=0.0, upper=100.0)
        .to_numpy(dtype=float)
    )

    cpu1_active = cpu1[active_traffic_mask]
    cpu2_active = cpu2[active_traffic_mask]
    if cpu1_active.size == 0 or cpu2_active.size == 0:
        print("[ERROR] Kolom CPU normalized kosong pada snapshot traffic aktif.")
        return

    avg_cpu1 = float(np.mean(cpu1_active))
    avg_cpu2 = float(np.mean(cpu2_active))
    avg_system_cpu = (avg_cpu1 + avg_cpu2) / 2.0
    std_cpu1 = float(np.std(cpu1_active, ddof=0))
    std_cpu2 = float(np.std(cpu2_active, ddof=0))

    # 7. Hitung Keseimbangan Per Snapshot (Instantaneous DI & BCU) dalam skala 0..1.
    # Karena CPU sudah normalized terhadap kapasitas masing-masing (0..100),
    # selisih maksimum antarnode adalah 100.
    balance_valid_mask, di_instant, bcu_instant = compute_di_bcu_series(cpu1, cpu2)
    balance_valid_mask = balance_valid_mask & active_traffic_mask

    di_values = di_instant[balance_valid_mask]
    bcu_values = bcu_instant[balance_valid_mask]
    mean_di = float(np.mean(di_values)) if di_values.size > 0 else 0.0
    mean_bcu = float(np.mean(bcu_values)) if bcu_values.size > 0 else 1.0
    macro_di, macro_bcu = compute_macro_di_bcu(avg_cpu1, avg_cpu2)

    # Base objective yang identik dengan notebook Colab:
    # - DI: mean(abs(cpu1 - cpu2) / 100.0) pada snapshot aktif
    # - ART: mean(((rt1 * req1) + (rt2 * req2)) / effective_requests) pada snapshot aktif
    base_art_per_row = np.zeros(len(df), dtype=float)
    base_art_per_row[active_traffic_mask] = (
        (node1_rt[active_traffic_mask] * node1_req[active_traffic_mask]) +
        (node2_rt[active_traffic_mask] * node2_req[active_traffic_mask])
    ) / np.maximum(effective_requests[active_traffic_mask], 1e-9)
    base_di = float(np.mean(di_instant[balance_valid_mask])) if di_values.size > 0 else 0.0
    base_art = float(np.mean(base_art_per_row[active_traffic_mask])) if np.any(active_traffic_mask) else 0.0

    # ================= CETAK LAPORAN =================
    print("=======================================================")
    print(" LAPORAN EVALUASI METRIK SKRIPSI (BERBASIS POST-FLIGHT)")
    print("=======================================================")
    print("File Dataset Dievaluasi: {}".format(file_path))
    print("Total Request Sukses   : {} request".format(int(total_requests)))
    print("-------------------------------------------------------")
    print("METRIK KINERJA (PERFORMANCE METRICS):")
    print(" 1. Makespan (Eq 2.4)  : {:.2f} detik".format(makespan))
    print(" 2. Response Time      : {:.2f} ms (global request-weighted)".format(avg_response_time))
    print(" 3. Throughput         : {:.2f} req/detik".format(throughput))
    print(" 4. Max RT (Global)    : {:.2f} ms".format(max_response_time))
    print(" 5. P99 RT (Global)    : {:.2f} ms".format(p99_response_time))
    print("-------------------------------------------------------")
    print("BASE OBJECTIVE KOMPATIBEL COLAB:")
    print(" -> Snapshot Base Obj  : {} baris (effective_requests > 0)".format(int(active_traffic_mask.sum())))
    print(" 6. Base DI            : {:.4f} (capacity-normalized; mean(|CPU1-CPU2|/100))".format(base_di))
    print(" 7. Base ART           : {:.2f} ms (mean(((RT1*Req1)+(RT2*Req2))/effective_requests))".format(base_art))
    print("-------------------------------------------------------")
    print("METRIK KESEIMBANGAN (BALANCING METRICS):")
    print(" -> Node 1 Avg CPU     : {:.2f}%".format(avg_cpu1))
    print(" -> Node 2 Avg CPU     : {:.2f}%".format(avg_cpu2))
    print(" -> Node 1 Std Dev CPU : {:.4f} (Semakin tinggi = semakin jungkat-jungkit)".format(std_cpu1))
    print(" -> Node 2 Std Dev CPU : {:.4f} (Semakin tinggi = semakin jungkat-jungkit)".format(std_cpu2))
    print(" -> Snapshot Aktif     : {} baris (effective_requests > 0)".format(int(active_traffic_mask.sum())))
    print(" -> Snapshot DI/BCU    : {} baris (aktif & CPU normalized tersedia)".format(int(balance_valid_mask.sum())))
    print(" 8. Res. Utilization   : {:.2f}% (Rata-rata sistem)".format(avg_system_cpu))
    print(" 9. Mean Inst. DI Ratio: {:.4f} (0 terbaik, 1 terburuk; |CPU1-CPU2|/100)".format(mean_di))
    print("10. Mean Inst. BCU     : {:.4f} (1 terbaik, 0 terburuk; 1-DI per snapshot)".format(mean_bcu))
    print("11. Macro DI Ratio     : {:.4f} (0 terbaik, 1 terburuk; |AvgCPU1-AvgCPU2|/100)".format(macro_di))
    print("12. Macro BCU          : {:.4f} (1 terbaik, 0 terburuk; 1-Macro DI)".format(macro_bcu))
    print("=======================================================")

if __name__ == "__main__":
    if len(sys.argv) > 1:
        csv_file = sys.argv[1]
        evaluate_csv(csv_file)
    else:
        print("Cara penggunaan: python3 calculate.py <nama_file.csv>")
        print("Contoh         : python3 calculate.py fbase.csv")
