package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/process" // Package baru untuk metrik spesifik container
)

var nodeName string
var myProcess *process.Process

// Inisialisasi proses Golang ini sebagai target pantauan CCTV (hanya jalan sekali)
func init() {
	var err error
	myProcess, err = process.NewProcess(int32(os.Getpid()))
	if err != nil {
		log.Printf("[WARNING] Gagal inisialisasi pembaca metrik Container: %v", err)
	}
}

type ResponseData struct {
	Message   string `json:"message"`
	NodeName  string `json:"node_name"`
	Status    string `json:"status"`
	RequestIP string `json:"request_ip"`
}

type NodeMetrics struct {
	NodeName     string  `json:"node_name"`
	CPUUsage     float64 `json:"cpu_usage"`      // Persentase CPU
	MemoryUsage  float64 `json:"memory_usage"`   // Persentase Memori
	LoadAverage1 float64 `json:"load_average_1"` // Rata-rata Beban 1 Menit
}

// HANDLER METRIK
func metricsHandler(w http.ResponseWriter, r *http.Request, name string) {
	// HOST_PROC sudah dihapus agar tidak bocor ke host OS

	var cpuUsage float64
	var memUsage float32

	if myProcess != nil {
		// 1. CPU Usage murni milik container ini
		cpuVal, err := myProcess.Percent(0)
		if err == nil {
			cpuUsage = cpuVal
			// Batasi maksimal 100% agar logika Fuzzy-PSO di Load Balancer tidak rusak
			if cpuUsage > 100.0 {
				cpuUsage = 100.0
			}
		}

		// 2. Memory Usage murni milik container ini
		memVal, err := myProcess.MemoryPercent()
		if err == nil {
			memUsage = memVal
		}
	}

	// 3. Load Average
	loadAvg1 := 0.0
	loadAvg, err := load.Avg()
	if err == nil {
		loadAvg1 = loadAvg.Load1
	}

	// respons metrik
	metrics := NodeMetrics{
		NodeName:     name,
		CPUUsage:     cpuUsage,
		MemoryUsage:  float64(memUsage),
		LoadAverage1: loadAvg1,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// POST /process -> Simulasi Upload & Komputasi Kriptografi (CPU Bound)
func dataProcessHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Gunakan method POST", http.StatusMethodNotAllowed)
		return
	}

	startTime := time.Now()

	// Baca data dari load generator
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Gagal membaca body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// SIMULASI BEBAN CPU: Hashing berulang 50 kali
	hashResult := ""
	for i := 0; i < 50; i++ {
		hash := sha256.Sum256(body)
		hashResult = fmt.Sprintf("%x", hash)
	}

	duration := time.Since(startTime)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "[%s] Sukses memproses %d KB. Waktu: %v | Hash: %s\n",
		nodeName, len(body)/1024, duration, hashResult[:10])
}

// GET /fetch -> Simulasi Download & String Builder (CPU & Memory Bound)
func dataFetchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Gunakan method GET", http.StatusMethodNotAllowed)
		return
	}

	sizeStr := r.URL.Query().Get("kb")
	sizeKB := 50 // Default 50 KB
	if s, err := strconv.Atoi(sizeStr); err == nil && s > 0 {
		sizeKB = s
	}

	if sizeKB > 500 {
		sizeKB = 500 // Maksimal 500 KB agar bandwidth aman
	}

	// SIMULASI BEBAN CPU: Membangun string besar
	var builder strings.Builder
	builder.Grow(sizeKB * 1024)

	baseString := "DATA-METRIK-SKRIPSI-PSO-"
	repeatCount := (sizeKB * 1024) / len(baseString)

	for i := 0; i < repeatCount; i++ {
		builder.WriteString(baseString)
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(builder.String()))
}

func main() {
	nName := flag.String("name", "API-NODE-UNKNOWN", "Nama unik untuk instance API node ini")
	flag.Parse()
	nodeName = *nName

	port := "8080"
	log.Printf("Starting API Service di port %s dengan nama: %s\n", port, nodeName)

	// Route Root
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := ResponseData{
			Message:   fmt.Sprintf("Request %s berhasil ditangani", r.Method),
			NodeName:  nodeName,
			Status:    "ok",
			RequestIP: r.RemoteAddr,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(data)
	})

	// Route Metrik
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metricsHandler(w, r, nodeName)
	})

	// Route Pengujian CPU (Untuk JMeter)
	http.HandleFunc("/process", dataProcessHandler)
	http.HandleFunc("/fetch", dataFetchHandler)

	// Mulai server
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Gagal memulai server di port %s: %v", port, err)
	}
}
