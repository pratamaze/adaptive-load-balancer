package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
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
			log.Println("[METRIC-COLLECTOR] Memulai pengambilan metrik periodik...")
			p.updateAllMetrics()
		}
	}()
}

// --- FUNGSI SELEKSI DIPERBARUI ---

// selectBackend_F_PSO_Framework kini HANYA MEMBACA metrik, super cepat!
func (p *NodePool) selectBackend_F_PSO_Framework() *Node {
	// Fungsi ini tidak lagi mengambil metrik, hanya membaca dari memori.
	// Ini membuatnya sangat cepat dan tidak menambah latensi pada request user.

	var bestNode *Node
	minCPU := math.MaxFloat64 // Nanti ganti dengan logika F-PSO Anda

	// --- DI SINILAH LOGIKA F-PSO ANDA AKAN DITEMPATKAN ---
	// Iterasi semua node dan baca metriknya
	for _, node := range p.nodes {
		// --- Gunakan RLock (Read Lock) ---
		node.mutex.RLock()
		// Ambil metrik yang sudah di-update oleh background collector
		currentCPU := node.CPUUsage
		// currentLoad := node.LoadAverage
		// currentLatency := node.ResponseTime
		node.mutex.RUnlock()
		// --- Selesai RLock ---

		// (Contoh logika: Pilih CPU terendah)
		if currentCPU < minCPU {
			minCPU = currentCPU
			bestNode = node
		}
	}
	// --- AKHIR DARI LOGIKA F-PSO ---

	if bestNode == nil {
		log.Println("[F-PSO] Peringatan: Tidak ada node yang valid, kembali ke node pertama.")
		if len(p.nodes) > 0 {
			bestNode = p.nodes[0] // Fallback
		} else {
			log.Println("[F-PSO] Error: Tidak ada node di pool!")
			return nil // Seharusnya tidak terjadi
		}
	}

	log.Printf("[F-PSO] Logika F-PSO memilih Node: %s (Metrik: CPU %.2f%%)\n", bestNode.Name, minCPU)
	return bestNode
}

// newReverseProxy membuat instance reverse proxy
func newReverseProxy(pool *NodePool) *httputil.ReverseProxy {
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Memilih backend menggunakan kerangka F-PSO
			// Ini sekarang SANGAT CEPAT
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
			log.Printf("[MONITOR] Menerima response %d dari %s\n", res.StatusCode, res.Request.URL.Host)
			return nil
		},

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("Gagal meneruskan request ke backend: %v\n", err)
			http.Error(w, "Service tidak tersedia", http.StatusServiceUnavailable)
		},
	}
	return proxy
}

func main() {
	// Inisialisasi daftar Node
	backendDNS := []string{
		"http://api-node1:8080",
		"http://api-node2:8080",
		// Tambahkan node lain di sini jika perlu
	}

	// Buat HTTP client khusus untuk metrik
	metricsClient := &http.Client{
		Timeout: 1500 * time.Millisecond, // Timeout 1.5 detik (lebih cepat dari interval)
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
	// Metrik akan di-update setiap 2 detik.
	pool.startMetricsCollector(2 * time.Second)
	// -------------------------

	// Membuat reverse proxy
	proxy := newReverseProxy(pool)

	log.Println("Memulai Load Balancer F-PSO (Mode Asinkron) di port :8080...")
	// Jalankan server proxy
	if err := http.ListenAndServe(":8080", proxy); err != nil {
		log.Fatalf("Gagal memulai server proxy: %v", err)
	}
}
