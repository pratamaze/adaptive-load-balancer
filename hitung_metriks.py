import re

def hitung_evaluasi(file_log):
    node1_cpus = []
    node2_cpus = []

    # 1. Buka dan baca file log baris per baris
    try:
        with open(file_log, 'r') as file:
            for line in file:
                # Hanya proses baris yang memiliki tag [DECISION]
                if "DECISION" in line:
                    # Ambil angka CPU menggunakan Regex
                    match1 = re.search(r'api-node1: CPU=([0-9.]+)%', line)
                    match2 = re.search(r'api-node2: CPU=([0-9.]+)%', line)
                    
                    if match1 and match2:
                        node1_cpus.append(float(match1.group(1)))
                        node2_cpus.append(float(match2.group(1)))
    except FileNotFoundError:
        print(f"File {file_log} tidak ditemukan!")
        return

    # Validasi jika log kosong
    if len(node1_cpus) == 0:
        print("Tidak ada data DECISION di dalam log.")
        return

    # 2. Hitung Rata-Rata CPU masing-masing node
    avg_cpu1 = sum(node1_cpus) / len(node1_cpus)
    avg_cpu2 = sum(node2_cpus) / len(node2_cpus)
    
    # Hitung Rata-Rata Total CPU (AvgCPU)
    avg_total = (avg_cpu1 + avg_cpu2) / 2

    # 3. Hitung Resource Utilization (RU)
    # Rata-rata pemakaian server secara keseluruhan (dalam persentase)
    ru = avg_total 

    # 4. Hitung Balanced CPU Utilization (BCU)
    # Menggunakan rumus deviasi absolut
    if avg_total == 0:
        bcu = 0 # Mencegah pembagian dengan 0 jika server benar-benar 0% terus
    else:
        dev_node1 = abs(avg_cpu1 - avg_total)
        dev_node2 = abs(avg_cpu2 - avg_total)
        total_deviasi = dev_node1 + dev_node2
        
        # N = 2 (karena ada 2 node)
        bcu = 1 - (total_deviasi / (2 * avg_total))

    # 5. Cetak Hasil
    print("="*40)
    print(" HASIL EVALUASI LOAD BALANCING (LOG)")
    print("="*40)
    print(f"Total Data Terekam : {len(node1_cpus)} sampel ({(len(node1_cpus)*0.5):.1f} detik)")
    print(f"Rata-rata CPU Node 1 : {avg_cpu1:.2f} %")
    print(f"Rata-rata CPU Node 2 : {avg_cpu2:.2f} %")
    print("-" * 40)
    print(f"Resource Utilization (RU) : {ru:.2f} %")
    print(f"Balanced CPU (BCU)        : {bcu:.4f} (Mendekati 1.0 = Sangat Seimbang)")
    print("="*40)

# Jalankan program
if __name__ == "__main__":
    # Ganti dengan nama file log kamu
    hitung_evaluasi("logs/hasil_pso_live.log")