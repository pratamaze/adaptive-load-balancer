import re

def hitung_evaluasi(file_log):
    node1_cpus = []
    node2_cpus = []
    node1_lats = []
    node2_lats = []

    # 1. Parsing File Log
    try:
        with open(file_log, 'r') as file:
            for line in file:
                if "DECISION" in line:
                    # Ambil CPU menggunakan Regex
                    cpu1 = re.search(r'api-node1: CPU=([0-9.]+)%', line)
                    cpu2 = re.search(r'api-node2: CPU=([0-9.]+)%', line)
                    
                    # Ambil Latency menggunakan Regex (Bonus untuk analisis skripsi)
                    lat1 = re.search(r'api-node1:.*Lat=([0-9.]+)ms', line)
                    lat2 = re.search(r'api-node2:.*Lat=([0-9.]+)ms', line)
                    
                    if cpu1 and cpu2:
                        node1_cpus.append(float(cpu1.group(1)))
                        node2_cpus.append(float(cpu2.group(1)))
                    if lat1 and lat2:
                        node1_lats.append(float(lat1.group(1)))
                        node2_lats.append(float(lat2.group(1)))

    except FileNotFoundError:
        print(f"File {file_log} tidak ditemukan!")
        return

    total_data = len(node1_cpus)
    if total_data == 0:
        print("Tidak ada data valid di dalam log.")
        return

    # 2. Hitung Rata-Rata CPU dan Latency
    avg_cpu1 = sum(node1_cpus) / total_data
    avg_cpu2 = sum(node2_cpus) / total_data
    
    avg_lat1 = sum(node1_lats) / total_data if node1_lats else 0
    avg_lat2 = sum(node2_lats) / total_data if node2_lats else 0

    avg_total_cpu = (avg_cpu1 + avg_cpu2) / 2

    # 3. Hitung Resource Utilization (RU)
    ru = avg_total_cpu 

    # 4. Hitung Balanced CPU Utilization (BCU)
    # Proteksi pembagian nol & kompensasi beban rendah
    if avg_total_cpu < 1.0:
        # Jika rata-rata CPU di bawah 1%, sistem dianggap 100% seimbang (idle/nganggur)
        bcu = 1.0
    else:
        dev_node1 = abs(avg_cpu1 - avg_total_cpu)
        dev_node2 = abs(avg_cpu2 - avg_total_cpu)
        total_deviasi = dev_node1 + dev_node2
        bcu = 1 - (total_deviasi / (2 * avg_total_cpu))

    # Proteksi jika BCU bernilai negatif (kasus ekstrem matematis)
    if bcu < 0:
        bcu = 0.0

    # 5. Cetak Laporan Analisis
    print("=" * 50)
    print(" LAPORAN EVALUASI LOAD BALANCER (LOG PARSER) 📊 ")
    print("=" * 50)
    print(f"Total Sampel Keputusan : {total_data} baris log")
    print(f"Estimasi Durasi Uji    : {(total_data * 0.5):.1f} detik")
    print("-" * 50)
    print("METRIK PER NODE (RATA-RATA):")
    print(f"   Node 1 -> CPU: {avg_cpu1:>6.2f}% | Latency: {avg_lat1:>8.2f} ms")
    print(f"   Node 2 -> CPU: {avg_cpu2:>6.2f}% | Latency: {avg_lat2:>8.2f} ms")
    print("-" * 50)
    print("METRIK SKRIPSI (EVALUASI KINERJA):")
    print(f"   Resource Utilization (RU) : {ru:.2f}%")
    if avg_total_cpu < 1.0:
        print(f"   Balanced CPU (BCU)        : {bcu:.4f} (Dikompensasi: Beban terlalu ringan)")
    else:
        print(f"   Balanced CPU (BCU)        : {bcu:.4f} (1.0 = Sangat Seimbang)")
    print("=" * 50)

if __name__ == "__main__":
    # Pastikan file hasil_pso_live.log berada di folder yang sama
    hitung_evaluasi("logs/hasil_pso_live.log")