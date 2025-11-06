package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	// Tetap dibutuhkan untuk cpu.Percent
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
)

// ResponseData adalah struktur untuk balasan JSON
type ResponseData struct {
	Message   string `json:"message"`
	NodeName  string `json:"node_name"`
	Status    string `json:"status"`
	RequestIP string `json:"request_ip"`
}

// NodeMetrics adalah struktur untuk melaporkan metrik
type NodeMetrics struct {
	NodeName     string  `json:"node_name"`
	CPUUsage     float64 `json:"cpu_usage"`      // Persentase CPU
	MemoryUsage  float64 `json:"memory_usage"`   // Persentase Memori
	LoadAverage1 float64 `json:"load_average_1"` // Rata-rata Beban 1 Menit
}

// metricsHandler mengambil metrik real-time dari host
func metricsHandler(w http.ResponseWriter, r *http.Request, nodeName string) {
	// Beri tahu gopsutil untuk membaca dari /host/proc (jika di dalam Docker)
	os.Setenv("HOST_PROC", "/host/proc")

	// 1. Dapatkan CPU Usage
	// --- PERBAIKAN KRUSIAL ---
	// Ubah interval ke 0 (non-blocking). Ini akan mengembalikan
	// persentase CPU sejak panggilan terakhir.
	cpuPercent, err := cpu.Percent(0, false)
	// -------------------------

	if err != nil {
		log.Printf("Error getting cpu: %v", err)
		http.Error(w, "Failed to get CPU", http.StatusInternalServerError)
		return
	}

	// 2. Dapatkan Memory Usage
	vm, err := mem.VirtualMemory()
	if err != nil {
		log.Printf("Error getting mem: %v", err)
		http.Error(w, "Failed to get Memory", http.StatusInternalServerError)
		return
	}

	// 3. Dapatkan Load Average
	loadAvg, err := load.Avg()
	if err != nil {
		log.Printf("Error getting load: %v", err)
		http.Error(w, "Failed to get Load", http.StatusInternalServerError)
		return
	}

	// Buat respons metrik
	metrics := NodeMetrics{
		NodeName:     nodeName,
		CPUUsage:     cpuPercent[0], // Ambil CPU gabungan
		MemoryUsage:  vm.UsedPercent,
		LoadAverage1: loadAvg.Load1,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func main() {
	// Parsing flag -name untuk identifikasi node
	nodeName := flag.String("name", "API-NODE-UNKNOWN", "Nama unik untuk instance API node ini")
	flag.Parse()

	port := "8080"
	log.Printf("Starting API Service di port %s dengan nama: %s\n", port, *nodeName)

	// Handler utama (untuk request biasa)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[%s] Menerima request: %s %s from %s\n", *nodeName, r.Method, r.URL.Path, r.RemoteAddr)

		data := ResponseData{
			Message:   fmt.Sprintf("Request %s berhasil ditangani", r.Method),
			NodeName:  *nodeName,
			Status:    "ok",
			RequestIP: r.RemoteAddr,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(data)
	})

	// Handler khusus untuk /metrics
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metricsHandler(w, r, *nodeName)
	})

	// Mulai server
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Gagal memulai server di port %s: %v", port, err)
	}
}
