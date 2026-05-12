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

	"github.com/shirou/gopsutil/v3/cpu"
)

var nodeName string
var hostName string
var telemetryState = struct {
	mu         sync.RWMutex
	cpuPercent float64
}{}

func init() {
	var err error
	hostName, err = os.Hostname()
	if err != nil || strings.TrimSpace(hostName) == "" {
		hostName = "unknown-host"
	}
}

type ResponseData struct {
	Message   string `json:"message"`
	NodeName  string `json:"node_name"`
	Status    string `json:"status"`
	RequestIP string `json:"request_ip"`
}

type CPUTelemetryResponse struct {
	NodeName      string  `json:"node_name"`
	CPUPercent    float64 `json:"cpu_percent"`
	SampledAtUnix int64   `json:"sampled_at_unix"`
}

func startCPUTelemetrySampler() {
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for range ticker.C {
			percentages, err := cpu.Percent(0, false)
			if err != nil || len(percentages) == 0 {
				continue
			}
			v := percentages[0]
			if v < 0 {
				v = 0
			}
			if v > 100 {
				v = 100
			}
			telemetryState.mu.Lock()
			telemetryState.cpuPercent = v
			telemetryState.mu.Unlock()
		}
	}()
}

func cpuTelemetryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Gunakan method GET", http.StatusMethodNotAllowed)
		return
	}
	telemetryState.mu.RLock()
	cpuPercent := telemetryState.cpuPercent
	telemetryState.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(CPUTelemetryResponse{
		NodeName:      nodeName,
		CPUPercent:    cpuPercent,
		SampledAtUnix: time.Now().Unix(),
	})
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

func withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		durationMS := float64(time.Since(start).Microseconds()) / 1000.0
		fullPath := r.URL.Path
		if r.URL.RawQuery != "" {
			fullPath += "?" + r.URL.RawQuery
		}
		log.Printf(
			"[API-ACCESS] backend=%s node=%s method=%s path=%s host=%s status=%d duration_ms=%.2f remote=%s ua=%q",
			hostName,
			nodeName,
			r.Method,
			fullPath,
			r.Host,
			rec.status,
			durationMS,
			r.RemoteAddr,
			r.UserAgent(),
		)
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
	startCPUTelemetrySampler()

	port := "8080"
	log.Printf("Starting API Service di port %s dengan nama: %s (hostname: %s)\n", port, nodeName, hostName)

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
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

	mux.HandleFunc("/process", dataProcessHandler)
	mux.HandleFunc("/write", dataProcessHandler)
	mux.HandleFunc("/fetch", dataFetchHandler)
	mux.HandleFunc("/read", dataFetchHandler)
	mux.HandleFunc("/api/stress-test", stressTestHandler)
	mux.HandleFunc("/telemetry/cpu", cpuTelemetryHandler)

	handler := chain(mux, withBackendHeader, withAccessLog)

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
