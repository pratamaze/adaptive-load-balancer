package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	// "os"
	// "encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"load-balancer/pkg/fuzzy"
	"load-balancer/pkg/pso"
	"load-balancer/pkg/roundrobin"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var lastRequestTime atomic.Int64

// Harus sama dengan yang ada di api-service
type NodeMetrics struct {
	NodeName     string  `json:"node_name"`
	CPUUsage     float64 `json:"cpu_usage"`
	MemoryUsage  float64 `json:"memory_usage"`
	LoadAverage1 float64 `json:"load_average_1"`
}

// backend service
type Node struct {
	Name string
	URL  *url.URL

	// Field metrik untuk F-PSO
	CPUUsage     float64
	LoadAverage  float64
	MemoryUsage  float64
	ResponseTime float64

	// lock field metrics
	mutex sync.RWMutex
}

// NodePool menampung node-node backend
type NodePool struct {
	nodes  []*Node
	client *http.Client // HTTP client khusus untuk memanggil /metrics
}

// 27 rules
var myRules = []fuzzy.Rule{
	// CPU RENDAH
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},

	// CPU SEDANG
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Rendah"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},

	// CPU TINGGI
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

// =====================================================================
// FUNGSI PEMBANTU F-PSO (Letakkan di atas func main)
// =====================================================================

// Membaca file pso_params.json jika sudah ada
func loadOptimizedParams(filename string, baseParams []float64) []float64 {
	data, err := os.ReadFile(filename)
	if err != nil {
		log.Printf("[INFO] File %s tidak ditemukan. Menggunakan parameter Fuzzy Dasar (Statis).", filename)
		return baseParams
	}

	var optimizedParams []float64
	if err := json.Unmarshal(data, &optimizedParams); err != nil {
		log.Printf("[WARNING] Gagal membaca %s. Menggunakan parameter Fuzzy Dasar.", filename)
		return baseParams
	}

	log.Printf("[SUCCESS] Berhasil memuat 27 Parameter Adaptif F-PSO dari %s!", filename)
	return optimizedParams
}

// Menyimpan 27 parameter gbest ke file secara real-time
func saveParamsToFile(filename string, params []float64) {
	file, err := os.Create(filename)
	if err != nil {
		log.Printf("[WARNING] Gagal membuat file konfigurasi: %v\n", err)
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(params); err != nil {
		log.Printf("[WARNING] Gagal menulis ke file JSON: %v\n", err)
	}
}

// Fungsi Fitness yang menggunakan data asli dari server saat itu juga
func evaluateFitnessRealtime(params []float64, n1, n2 *Node) float64 {
	dummyEngine := fuzzy.NewEngine(params)

	// Gunakan metrik asli saat itu juga
	score1 := dummyEngine.CalculateMamdani(fuzzy.NodeMetrics{CPU: n1.CPUUsage, QueueLength: n1.LoadAverage * 10, RespTime: n1.ResponseTime}, myRules)
	score2 := dummyEngine.CalculateMamdani(fuzzy.NodeMetrics{CPU: n2.CPUUsage, QueueLength: n2.LoadAverage * 10, RespTime: n2.ResponseTime}, myRules)

	loadDiff := n1.CPUUsage - n2.CPUUsage
	scoreDiff := score1 - score2

	return -(loadDiff * scoreDiff)
}

// =====================================================================

// func main() {
// ... (isi fungsi main kamu yang sudah benar tadi) ...

// 27 Parameter Baseline ( 3 variabel x 3 label x 3 nilai A,B,C)
var BaseFuzzyParams = []float64{
	// CPU (Rendah, Sedang, Tinggi)
	0, 0, 50, 0, 50, 100, 50, 100, 100,
	// Queue (Rendah, Sedang, Tinggi)
	0, 0, 500, 0, 500, 1000, 500, 1000, 1000,
	// RespTime (Cepat, Normal, Lambat)
	0, 0, 500, 0, 500, 1000, 500, 1000, 1000,
}

// DUA MESIN TERPISAH: 1 Statis (Variabel Kontrol), 1 Dinamis (Dioptimasi PSO)
var StaticFuzzyEngine = fuzzy.NewEngine(BaseFuzzyParams)
var AdaptiveFPSOEngine = fuzzy.NewEngine(BaseFuzzyParams)

// paralel per node
func (p *NodePool) getRealNodeMetrics(node *Node) {
	metricsURL := node.URL.String() + "/metrics"
	startTime := time.Now()

	req, _ := http.NewRequest("GET", metricsURL, nil)
	resp, err := p.client.Do(req)

	// respontime dalam ms
	responseTime := time.Since(startTime).Seconds() * 1000

	// Write Lock
	node.mutex.Lock()
	defer node.mutex.Unlock()

	// penalti
	if err != nil {
		log.Printf("[METRIC] Gagal mengambil metrik dari %s: %v\n", node.Name, err)
		node.CPUUsage = 100.0
		node.ResponseTime = 99999.0
		return
	}
	defer resp.Body.Close()

	// sukses
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

	// Update metrik node
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

// update paralel ke SEMUA node
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

// goroutine di background untuk mengambil metrik
func (p *NodePool) startMetricsCollector(interval time.Duration) {
	log.Printf("[METRIC-COLLECTOR] Memulai kolektor metrik (setiap %s)\n", interval)
	p.updateAllMetrics()

	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			log.Println()
			log.Println("[METRIC-COLLECTOR] Memulai pengambilan metrik periodik...")
			p.updateAllMetrics()
		}
	}()
}

// evaluateFitness menilai seberapa bagus 27 angka tebakan PSO untuk menyeimbangkan beban
func evaluateFitness(params []float64, n1, n2 *Node) float64 {
	// Buat mesin bayangan sementara
	dummyEngine := fuzzy.NewEngine(params)
	score1 := dummyEngine.CalculateMamdani(fuzzy.NodeMetrics{CPU: n1.CPUUsage, QueueLength: n1.LoadAverage * 10, RespTime: n1.ResponseTime}, myRules)
	score2 := dummyEngine.CalculateMamdani(fuzzy.NodeMetrics{CPU: n2.CPUUsage, QueueLength: n2.LoadAverage * 10, RespTime: n2.ResponseTime}, myRules)

	// Jika CPU N1 lebih besar dari N2 (selisih positif), maka SKOR N1 harus lebih kecil dari N2 (selisih negatif).
	loadDiff := n1.CPUUsage - n2.CPUUsage
	scoreDiff := score1 - score2

	// Hasil kali loadDiff dan scoreDiff harus negatif jika arahnya benar.
	// Agar fitness makin besar makin baik, kita beri tanda minus di depannya.
	return -(loadDiff * scoreDiff)
}

var rrBalancer = roundrobin.New()

func (p *NodePool) selectBackend_RoundRobin() *Node {
	totalNodes := len(p.nodes)
	if totalNodes == 0 {
		return nil
	}

	// Thread-Safe
	idx := rrBalancer.NextIndex(totalNodes)
	selectedNode := p.nodes[idx]

	// log
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		cpu := node.CPUUsage
		queue := node.LoadAverage * 10
		lat := node.ResponseTime
		node.mutex.RUnlock()

		// Skor diset 0.0000 karena RR tidak menghitung kelayakan.
		detailLog += fmt.Sprintf("[%s: CPU=%.2f%%, Q=%.2f, Lat=%.2fms -> Skor=0.0000] ",
			node.Name, cpu, queue, lat)
	}

	// log komprehensif untuk evaluasi kinerja Python
	log.Printf("[DECISION] %s==> TERPILIH: %s\n", detailLog, selectedNode.Name)

	return selectedNode
}

// ALGORITMA 2: FUZZY STATIS MURNI
func (p *NodePool) selectBackend_Fuzzy_Static() *Node {
	var bestNode *Node
	maxScore := -1.0
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		metrics := fuzzy.NodeMetrics{CPU: node.CPUUsage, QueueLength: node.LoadAverage * 10, RespTime: node.ResponseTime}
		node.mutex.RUnlock()

		// PANGGIL MESIN STATIS
		score := StaticFuzzyEngine.CalculateMamdani(metrics, myRules)
		detailLog += fmt.Sprintf("[%s: CPU=%.2f%%, Q=%.2f, Lat=%.2fms -> Skor=%.4f] ", node.Name, metrics.CPU, metrics.QueueLength, metrics.RespTime, score)

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
		log.Printf("[DECISION] %s ==> TERPILIH (FUZZY): %s\n", detailLog, bestNode.Name)
	}
	return bestNode
}

// ALGORITMA 3: ADAPTIVE F-PSO (Ini boss akhirnya)
func (p *NodePool) selectBackend_FPSO_Adaptive() *Node {
	var bestNode *Node
	maxScore := -1.0
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		metrics := fuzzy.NodeMetrics{CPU: node.CPUUsage, QueueLength: node.LoadAverage * 10, RespTime: node.ResponseTime}
		node.mutex.RUnlock()

		// PANGGIL MESIN F-PSO
		score := AdaptiveFPSOEngine.CalculateMamdani(metrics, myRules)
		detailLog += fmt.Sprintf("[%s: CPU=%.2f%%, Q=%.2f, Lat=%.2fms -> Skor=%.4f] ", node.Name, metrics.CPU, metrics.QueueLength, metrics.RespTime, score)

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
		log.Printf("[DECISION] %s ==> TERPILIH (F-PSO): %s\n", detailLog, bestNode.Name)
	}
	return bestNode
}

func (p *NodePool) startPSOOptimizer(interval time.Duration) {
	log.Printf("[PSO-WORKER] Optimizer F-PSO berjalan otomatis HANYA saat ada trafik load test...\n")
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			if len(p.nodes) < 2 {
				continue
			}

			// =========================================================
			// 🛡️ DETEKSI LOAD TEST OTOMATIS BERDASARKAN TRAFIK
			// =========================================================
			now := time.Now().UnixNano()
			lastReq := lastRequestTime.Load()

			// Jika selisih waktu dari request terakhir lebih dari 5 detik,
			// berarti JMeter sudah dimatikan/idle. Lewati proses belajar!
			if (now - lastReq) > (5 * time.Second).Nanoseconds() {
				continue
			}
			// =========================================================

			// 1. Ambil data saat ini (hanya tereksekusi saat JMeter aktif)
			p.nodes[0].mutex.RLock()
			n1 := *p.nodes[0]
			p.nodes[0].mutex.RUnlock()
			p.nodes[1].mutex.RLock()
			n2 := *p.nodes[1]
			p.nodes[1].mutex.RUnlock()

			// 2. Jalankan PSO
			currentParams := AdaptiveFPSOEngine.GetParams()
			swarm := pso.NewSwarm(currentParams, func(params []float64) float64 {
				return evaluateFitnessRealtime(params, &n1, &n2)
			})
			bestParams := swarm.Optimize()

			// 3. Terapkan & Simpan
			AdaptiveFPSOEngine.UpdateParams(bestParams)
			saveParamsToFile("/logs/pso_params.json", bestParams)
			log.Println("[PSO] Model beradaptasi dari trafik masuk dan disimpan ke /logs/pso_params.json")
		}
	}()
}

// instance reverse proxy
func newReverseProxy(pool *NodePool) *httputil.ReverseProxy {

	// custom for performance
	customTransport := &http.Transport{
		MaxIdleConns:          10000, // Total koneksi keseluruhan
		MaxIdleConnsPerHost:   5000,  // Batas koneksi nganggur per backend
		MaxConnsPerHost:       15000, // Batas koneksi aktif per backend
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	proxy := &httputil.ReverseProxy{
		Transport: customTransport,
		Director: func(req *http.Request) {

			lastRequestTime.Store(time.Now().UnixNano())

			// backendNode := pool.selectBackend_F_PSO_Framework()
			// backendNode := pool.selectBackend_RoundRobin()
			// backendNode := pool.selectBackend_Fuzzy_Static() // TAG: :fuzzy
			backendNode := pool.selectBackend_FPSO_Adaptive()

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
	// =====================================================================
	// 1. SETUP LOGGING (Menulis log ke terminal DAN ke folder /logs)
	// =====================================================================
	// Membuka atau membuat file hasil_fpso.log di folder /logs
	logFile, err := os.OpenFile("/logs/hasil_fpso.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Printf("Gagal membuka file log di /logs: %v. Log hanya tampil di terminal.", err)
	} else {
		defer logFile.Close()
		multiWriter := io.MultiWriter(os.Stdout, logFile)
		log.SetOutput(multiWriter)
	}

	// =====================================================================
	// 2. INISIALISASI OTAK FUZZY & F-PSO (Sangat Krusial!)
	// =====================================================================
	var BaseFuzzyParams = []float64{
		// CPU (Rendah, Sedang, Tinggi)
		0, 0, 50, 0, 50, 100, 50, 100, 100,
		// Queue (Rendah, Sedang, Tinggi)
		0, 0, 500, 0, 500, 1000, 500, 1000, 1000,
		// Latency (Cepat, Normal, Lambat)
		0, 0, 500, 0, 500, 1000, 500, 1000, 1000,
	}

	// Buat Mesin Statis (Pembanding)
	StaticFuzzyEngine = fuzzy.NewEngine(BaseFuzzyParams)

	// Coba muat parameter yang sudah pintar dari file (jika ada)
	activePSOParams := loadOptimizedParams("/logs/pso_params.json", BaseFuzzyParams)

	// Buat Mesin Adaptif menggunakan parameter tersebut
	AdaptiveFPSOEngine = fuzzy.NewEngine(activePSOParams)

	// =====================================================================
	// 3. INISIALISASI NODE BACKEND
	// =====================================================================
	backendDNS := []string{
		"http://api-node1:8080",
		"http://api-node2:8080",
	}

	metricsClient := &http.Client{
		Timeout: 500 * time.Millisecond,
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

	// =====================================================================
	// 4. MENJALANKAN BACKGROUND WORKER (Metrik & PSO)
	// =====================================================================
	pool.startMetricsCollector(200 * time.Millisecond)

	// PSO berjalan setiap 5 detik, belajar, lalu menyimpan ke /logs/pso_params.json
	pool.startPSOOptimizer(5 * time.Second)

	// =====================================================================
	// 5. MENJALANKAN SERVER REVERSE PROXY
	// =====================================================================
	mux := http.NewServeMux()

	// Daftarkan endpoint metrik untuk Prometheus
	mux.Handle("/metrics", promhttp.Handler())

	// Buat instance proxy yang di dalamnya sudah ada "Saklar" algoritma
	proxy := newReverseProxy(pool)

	// Handler utama
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Pengecekan manual agar path /metrics tidak nyasar ke backend
		if r.URL.Path == "/metrics" {
			promhttp.Handler().ServeHTTP(w, r)
			return
		}
		// Jalankan proxy untuk trafik HTTP lainnya (JMeter)
		proxy.ServeHTTP(w, r)
	})

	log.Println("Memulai Load Balancer F-PSO di port :8080...")

	// Konfigurasi server dengan timeout agar tangguh saat diserang JMeter
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
