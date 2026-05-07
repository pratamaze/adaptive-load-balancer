import pandas as pd
import sys
import numpy as np
import warnings

# Matikan warning dari pandas biar output terminal bersih
warnings.filterwarnings('ignore')

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

    # 4. Hitung Response Time Aktual 
    # Karena di per_hit mode, request masuk ke salah satu node (node1_requests=1 atau node2_requests=1)
    actual_latency = np.where(df['node1_requests'] > 0, 
                              df['node1_response_ms'], 
                              df['node2_response_ms'])
    avg_response_time = np.mean(actual_latency)

    # 5. Hitung Rata-rata CPU Aktual (Normalized)
    avg_cpu1 = df['node1_cpu_normalized_usage'].mean()
    avg_cpu2 = df['node2_cpu_normalized_usage'].mean()
    avg_system_cpu = (avg_cpu1 + avg_cpu2) / 2.0

    # 6. Hitung Keseimbangan (DI & BCU)
    if avg_system_cpu > 0:
        di = abs(avg_cpu1 - avg_cpu2) / avg_system_cpu
        dev1 = abs(avg_cpu1 - avg_system_cpu)
        dev2 = abs(avg_cpu2 - avg_system_cpu)
        bcu = 1.0 - ((dev1 + dev2) / (2.0 * avg_system_cpu))
    else:
        di = 0.0
        bcu = 1.0

    # Pastikan BCU berada di rentang 0 - 1
    bcu = max(0.0, min(1.0, bcu))

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
    print("-------------------------------------------------------")
    print("METRIK KESEIMBANGAN (BALANCING METRICS):")
    print(f" -> Node 1 Avg CPU     : {avg_cpu1:.2f}%")
    print(f" -> Node 2 Avg CPU     : {avg_cpu2:.2f}%")
    print(f" 4. Res. Utilization   : {avg_system_cpu:.2f}% (Rata-rata sistem)")
    print(f" 5. Degree Imbal. (DI) : {di:.4f} (Mendekati 0 = Sempurna)")
    print(f" 6. Balanced CPU (BCU) : {bcu:.4f} (Mendekati 1 = Sempurna)")
    print("=======================================================")

if __name__ == "__main__":
    if len(sys.argv) > 1:
        csv_file = sys.argv[1]
        evaluate_csv(csv_file)
    else:
        print("Cara penggunaan: python3 calculate.py <nama_file.csv>")
        print("Contoh         : python3 calculate.py fbase.csv")