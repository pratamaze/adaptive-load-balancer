package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/process"
)

var nodeName string
var hostName string
var myProcess *process.Process
var (
	cachedCPU  float64
	cpuMutex   sync.RWMutex
	cpuHistory []float64
)

var (
	apiRequestTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_http_requests_total",
			Help: "Total request HTTP per replica API",
		},
		[]string{"backend_server", "node_name", "method", "path", "status"},
	)
	apiRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "api_http_request_duration_seconds",
			Help:    "Durasi request HTTP per replica API",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"backend_server", "node_name", "method", "path", "status"},
	)
)

func init() {
	prometheus.MustRegister(apiRequestTotal)
	prometheus.MustRegister(apiRequestDuration)

	var err error
	hostName, err = os.Hostname()
	if err != nil || strings.TrimSpace(hostName) == "" {
		hostName = "unknown-host"
	}

	myProcess, err = process.NewProcess(int32(os.Getpid()))
	if err != nil {
		log.Printf("[WARNING] Gagal inisialisasi pembaca metrik Container: %v", err)
	}

	if myProcess != nil {
		myProcess.Percent(0)
		go startCPUMonitor()
	}
}

func startCPUMonitor() {
	ticker := time.NewTicker(200 * time.Millisecond)
	const windowSize = 5

	limitStr := os.Getenv("CPU_LIMIT_PERCENT")
	cpuLimit, err := strconv.ParseFloat(limitStr, 64)
	if err != nil || cpuLimit <= 0 {
		cpuLimit = 100.0
	}

	for range ticker.C {
		val, err := myProcess.Percent(0)
		if err == nil {
			scaledVal := (val / cpuLimit) * 100.0
			if scaledVal > 100.0 {
				scaledVal = 100.0
			}

			cpuMutex.Lock()
			cpuHistory = append(cpuHistory, scaledVal)
			if len(cpuHistory) > windowSize {
				cpuHistory = cpuHistory[1:]
			}
			sum := 0.0
			for _, v := range cpuHistory {
				sum += v
			}
			cachedCPU = sum / float64(len(cpuHistory))
			cpuMutex.Unlock()
		}
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
	CPUUsage     float64 `json:"cpu_usage"`
	MemoryUsage  float64 `json:"memory_usage"`
	LoadAverage1 float64 `json:"load_average_1"`
}

func metricsJSONHandler(w http.ResponseWriter, r *http.Request, name string) {
	var cpuUsage float64
	var memUsage float32

	if myProcess != nil {
		cpuMutex.RLock()
		cpuUsage = cachedCPU
		cpuMutex.RUnlock()

		memVal, err := myProcess.MemoryPercent()
		if err == nil {
			memUsage = memVal
		}
	}

	loadAvg1 := 0.0
	loadAvg, err := load.Avg()
	if err == nil {
		loadAvg1 = loadAvg.Load1
	}

	metrics := NodeMetrics{
		NodeName:     name,
		CPUUsage:     cpuUsage,
		MemoryUsage:  float64(memUsage),
		LoadAverage1: loadAvg1,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(metrics)
}

func dataProcessHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Gunakan method POST", http.StatusMethodNotAllowed)
		return
	}

	startTime := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Gagal membaca body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	var hash [32]byte
	for i := 0; i < 6000; i++ {
		hash = sha256.Sum256(body)
	}

	duration := time.Since(startTime)
	hashResult := fmt.Sprintf("%x", hash)
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "[%s] Sukses memproses %d KB. Waktu: %v | Hash: %s\n", nodeName, len(body)/1024, duration, hashResult[:10])
}

func dataFetchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Gunakan method GET", http.StatusMethodNotAllowed)
		return
	}

	sizeStr := r.URL.Query().Get("kb")
	sizeKB := 50
	if s, err := strconv.Atoi(sizeStr); err == nil && s > 0 {
		sizeKB = s
	}
	if sizeKB > 500 {
		sizeKB = 500
	}

	var builder strings.Builder
	builder.Grow(sizeKB * 1024)

	baseString := "DATA-METRIK-SKRIPSI-PSO-"
	repeatCount := (sizeKB * 1024) / len(baseString)
	for i := 0; i < repeatCount; i++ {
		builder.WriteString(baseString)
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(builder.String()))
}

type StressResponse struct {
	NodeName       string  `json:"node_name"`
	BackendServer  string  `json:"backend_server"`
	TargetMS       int     `json:"target_ms"`
	DurationMS     float64 `json:"duration_ms"`
	PrimesComputed int     `json:"primes_computed"`
	LastPrime      int     `json:"last_prime"`
}

func isPrime(n int) bool {
	if n < 2 {
		return false
	}
	if n == 2 {
		return true
	}
	if n%2 == 0 {
		return false
	}
	limit := int(math.Sqrt(float64(n)))
	for i := 3; i <= limit; i += 2 {
		if n%i == 0 {
			return false
		}
	}
	return true
}

// GET /api/stress-test
// Beban CPU sintetis terkontrol untuk memicu contention (default ~75ms/request).
func stressTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Gunakan method GET", http.StatusMethodNotAllowed)
		return
	}

	targetMS := 75
	if q := r.URL.Query().Get("ms"); q != "" {
		if v, err := strconv.Atoi(q); err == nil {
			targetMS = v
		}
	}
	if targetMS < 50 {
		targetMS = 50
	}
	if targetMS > 150 {
		targetMS = 150
	}

	start := time.Now()
	deadline := start.Add(time.Duration(targetMS) * time.Millisecond)

	candidate := 2
	primesComputed := 0
	lastPrime := 2
	for time.Now().Before(deadline) {
		if isPrime(candidate) {
			lastPrime = candidate
			primesComputed++
		}
		candidate++
	}

	resp := StressResponse{
		NodeName:       nodeName,
		BackendServer:  hostName,
		TargetMS:       targetMS,
		DurationMS:     float64(time.Since(start).Microseconds()) / 1000.0,
		PrimesComputed: primesComputed,
		LastPrime:      lastPrime,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func withBackendHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Server", hostName)
		next.ServeHTTP(w, r)
	})
}

func withPrometheus(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		statusStr := strconv.Itoa(rec.status)
		labels := []string{hostName, nodeName, r.Method, r.URL.Path, statusStr}
		apiRequestTotal.WithLabelValues(labels...).Inc()
		apiRequestDuration.WithLabelValues(labels...).Observe(time.Since(start).Seconds())
	})
}

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func main() {
	nName := flag.String("name", "API-NODE-UNKNOWN", "Nama unik untuk instance API node ini")
	flag.Parse()
	nodeName = *nName

	port := "8080"
	log.Printf("Starting API Service di port %s dengan nama: %s (hostname: %s)\n", port, nodeName, hostName)

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := ResponseData{
			Message:   fmt.Sprintf("Request %s berhasil ditangani", r.Method),
			NodeName:  nodeName,
			Status:    "ok",
			RequestIP: r.RemoteAddr,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(data)
	})

	// Endpoint JSON internal untuk load-balancer collector.
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metricsJSONHandler(w, r, nodeName)
	})

	// Endpoint Prometheus untuk observability per replika.
	mux.Handle("/metrics/prometheus", promhttp.Handler())

	mux.HandleFunc("/process", dataProcessHandler)
	mux.HandleFunc("/fetch", dataFetchHandler)
	mux.HandleFunc("/api/stress-test", stressTestHandler)

	handler := chain(mux, withBackendHeader, withPrometheus)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Gagal memulai server di port %s: %v", port, err)
	}
}
