package main

import (
	"encoding/json"
	"fmt"
	"io"
	"load-balancer/pkg/fuzzy"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Harus sama dengan yang ada di api-service
type NodeMetrics struct {
	NodeName     string  `json:"node_name"`
	CPUUsage     float64 `json:"cpu_usage"`
	MemoryUsage  float64 `json:"memory_usage"`
	LoadAverage1 float64 `json:"load_average_1"`
}

// Node merepresentasikan backend service
type Node struct {
	Name string
	URL  *url.URL
	// Field metrik untuk F-PSO, dilindungi oleh Mutex
	CPUUsage     float64
	LoadAverage  float64
	MemoryUsage  float64
	ResponseTime float64

	mutex sync.RWMutex // Melindungi field metrik di atas
}

// NodePool menampung node-node backend
type NodePool struct {
	nodes  []*Node
	client *http.Client // HTTP client khusus untuk memanggil /metrics
}

// 27 rules
// 27 Aturan Lengkap F-PSO Load Balancer
var myRules = []fuzzy.Rule{
	// --- KONDISI CPU RENDAH ---
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},

	// --- KONDISI CPU SEDANG ---
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Rendah"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},

	// --- KONDISI CPU TINGGI ---
	{CPULabel: "Tinggi", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Tinggi", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Tinggi", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Tinggi", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},
}

// getRealNodeMetrics mengambil data dari endpoint /metrics dan mengupdate SATU node
// Fungsi ini dipanggil oleh background collector
func (p *NodePool) getRealNodeMetrics(node *Node) {
	metricsURL := node.URL.String() + "/metrics"
	startTime := time.Now()

	req, _ := http.NewRequest("GET", metricsURL, nil)
	resp, err := p.client.Do(req)

	responseTime := time.Since(startTime).Seconds() * 1000 // dalam ms

	// --- Gunakan Lock (Write Lock) ---
	node.mutex.Lock()
	defer node.mutex.Unlock()

	// Jika GAGAL (node mati, timeout, dll.)
	if err != nil {
		log.Printf("[METRIC] Gagal mengambil metrik dari %s: %v\n", node.Name, err)
		// Beri penalti agar tidak dipilih oleh F-PSO
		node.CPUUsage = 100.0       // Set CPU ke max
		node.ResponseTime = 99999.0 // Set latensi ke max
		return
	}
	defer resp.Body.Close()

	// Jika SUKSESshut
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[METRIC] Gagal membaca body dari %s: %v\n", node.Name, err)
		return
	}

	var metrics NodeMetrics
	if err := json.Unmarshal(body, &metrics); err != nil {
		log.Printf("[METRIC] Gagal parse JSON dari %s: %v\n", node.Name, err)
		return
	}

	// Update metrik di node
	node.CPUUsage = metrics.CPUUsage
	node.LoadAverage = metrics.LoadAverage1
	node.MemoryUsage = metrics.MemoryUsage
	node.ResponseTime = responseTime // Waktu respon real dari panggilan /metrics

	// Kirim data terbaru ke Prometheus Exporter
	cpuGauge.WithLabelValues(node.Name).Set(metrics.CPUUsage)
	latencyGauge.WithLabelValues(node.Name).Set(responseTime)

	log.Printf("[METRIC] Node %s: CPU=%.2f%%, Load=%.2f, Latency=%.2fms\n",
		node.Name, node.CPUUsage, node.LoadAverage, node.ResponseTime)
}

// --- FUNGSI BARU UNTUK BACKGROUND COLLECTOR ---

// updateAllMetrics memicu update paralel ke SEMUA node
func (p *NodePool) updateAllMetrics() {
	var wg sync.WaitGroup
	for _, node := range p.nodes {
		wg.Add(1)
		go func(n *Node) {
			defer wg.Done()
			p.getRealNodeMetrics(n)
		}(node)
	}
	wg.Wait()
}

// startMetricsCollector memulai goroutine di background untuk mengambil metrik
func (p *NodePool) startMetricsCollector(interval time.Duration) {
	log.Printf("[METRIC-COLLECTOR] Memulai kolektor metrik (setiap %s)\n", interval)

	// Jalankan satu kali di awal agar data tidak kosong saat server baru menyala
	p.updateAllMetrics()

	// Jalankan ticker di goroutine terpisah
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			log.Println()
			log.Println("[METRIC-COLLECTOR] Memulai pengambilan metrik periodik...")
			p.updateAllMetrics()
		}
	}()
}

// --- FUNGSI SELEKSI DIPERBARUI ---

func (p *NodePool) selectBackend_F_PSO_Framework() *Node {
	var bestNode *Node
	maxScore := -1.0

	// Variabel string untuk merangkum detail perhitungan semua node
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		metrics := fuzzy.NodeMetrics{
			CPU:         node.CPUUsage,
			QueueLength: node.LoadAverage * 10,
			RespTime:    node.ResponseTime,
		}
		node.mutex.RUnlock()

		score := fuzzy.CalculateMamdani(metrics, myRules)

		// Rangkum hitungan eksak ke dalam string
		detailLog += fmt.Sprintf("[%s: CPU=%.2f%%, Q=%.2f, Lat=%.2fms -> Skor=%.4f] ",
			node.Name, metrics.CPU, metrics.QueueLength, metrics.RespTime, score)

		if score > maxScore {
			maxScore = score
			bestNode = node
		} else if score == maxScore && bestNode != nil {
			node.mutex.RLock()
			bestNodeCPU := bestNode.CPUUsage
			node.mutex.RUnlock()

			if metrics.CPU < bestNodeCPU {
				bestNode = node
			}
		}
	}

	if bestNode != nil {
		// Cetak satu baris log komprehensif untuk bukti pengujian
		log.Printf("[DECISION] %s ==> TERPILIH: %s\n", detailLog, bestNode.Name)
	}

	return bestNode
}

// newReverseProxy membuat instance reverse proxy
func newReverseProxy(pool *NodePool) *httputil.ReverseProxy {

	customTransport := &http.Transport{
		MaxIdleConns:          10000, // Total koneksi keseluruhan
		MaxIdleConnsPerHost:   5000,  // Batas koneksi nganggur per backend
		MaxConnsPerHost:       5000,  // Batas koneksi aktif per backend
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	proxy := &httputil.ReverseProxy{
		Transport: customTransport, // 2. Pasang transport ini ke proxy Anda
		Director: func(req *http.Request) {
			backendNode := pool.selectBackend_F_PSO_Framework()

			if backendNode == nil {
				log.Println("Gagal memilih backend, tidak ada node tersedia.")
				return
			}

			// Mengarahkan request ke node yang dipilih
			req.URL.Scheme = backendNode.URL.Scheme
			req.URL.Host = backendNode.URL.Host
			req.URL.Path = req.URL.Path
			req.Host = backendNode.URL.Host
		},

		ModifyResponse: func(res *http.Response) error {
			// log.Println()
			// log.Printf("[MONITOR] Menerima response %d dari %s\n", res.StatusCode, res.Request.URL.Host)
			// log.Println()
			return nil
		},

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("Gagal meneruskan request ke backend: %v\n", err)
			http.Error(w, "Service tidak tersedia", http.StatusServiceUnavailable)
		},
	}
	return proxy
}

var (
	// Wadah untuk metrik CPU per node
	cpuGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pso_node_cpu_usage",
			Help: "Penggunaan CPU node backend (%)",
		},
		[]string{"node_name"},
	)
	// Wadah untuk metrik Latensi per node
	latencyGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pso_node_latency_ms",
			Help: "Latensi komunikasi ke node (ms)",
		},
		[]string{"node_name"},
	)
)

func init() {
	// Mendaftarkan metrik ke sistem Prometheus
	prometheus.MustRegister(cpuGauge)
	prometheus.MustRegister(latencyGauge)
}

func main() {

	// --- TAMBAHAN UNTUK MENYIMPAN LOG KE FILE ---
	// Membuat folder /logs jika belum ada
	os.MkdirAll("/logs", os.ModePerm)

	// Membuka atau membuat file pso_evaluation.log
	logFile, err := os.OpenFile("/logs/pso_evaluation.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Gagal membuka file log: %v", err)
	}
	defer logFile.Close()

	// Log muncul di terminal DAN ditulis ke file
	multiWriter := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(multiWriter)

	// Inisialisasi daftar Node
	backendDNS := []string{
		"http://api-node1:8080",
		"http://api-node2:8080",
	}

	//  HTTP client khusus untuk metrik
	metricsClient := &http.Client{
		Timeout: 500 * time.Millisecond, // Timeout 0.5 detik (lebih cepat dari interval)
	}

	pool := &NodePool{client: metricsClient}
	for i, dns := range backendDNS {
		backendURL, err := url.Parse(dns)
		if err != nil {
			log.Fatalf("Gagal mem-parse URL backend: %v", err)
		}
		nodeName := fmt.Sprintf("api-node%d", i+1)

		pool.nodes = append(pool.nodes, &Node{
			Name: nodeName,
			URL:  backendURL,
		})
		log.Printf("Mendaftarkan backend node: %s di %s\n", nodeName, backendURL)
	}

	// --- PERUBAHAN KRUSIAL ---
	// Jalankan kolektor metrik di background.
	// Metrik akan di-update setiap
	pool.startMetricsCollector(500 * time.Millisecond)
	// -------------------------

	// 1. Inisialisasi Mux
	mux := http.NewServeMux()

	// Gunakan full path agar tidak tertukar
	mux.Handle("/metrics", promhttp.Handler())

	// 3. Buat handler untuk proxy
	proxy := newReverseProxy(pool)

	//  HandleFunc untuk "/" tapi beri pengecekan manual
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Jika request datang ke /metrics tapi tidak sengaja masuk ke sini
		if r.URL.Path == "/metrics" {
			promhttp.Handler().ServeHTTP(w, r)
			return
		}
		// Jalankan proxy untuk trafik lainnya
		proxy.ServeHTTP(w, r)
	})

	log.Println("Memulai Load Balancer di port :8080...")

	//  konfigurasi server dengan timeout agar tidak hang selamanya
	server := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Gagal memulai server: %v", err)
	}
}
