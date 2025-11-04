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
	// Field metrik untuk F-PSO
	CPUUsage     float64
	LoadAverage  float64
	MemoryUsage  float64
	ResponseTime float64

	mutex sync.RWMutex // Melindungi field metrik
}

// NodePool menampung node-node backend
type NodePool struct {
	nodes  []*Node
	client *http.Client // HTTP client untuk memanggil /metrics
}

// getRealNodeMetrics mengambil data dari endpoint /metrics
func (p *NodePool) getRealNodeMetrics(node *Node) {
	// Bangun URL metrics, cth: http://api-node1:8080/metrics
	metricsURL := node.URL.String() + "/metrics"
	startTime := time.Now()

	req, _ := http.NewRequest("GET", metricsURL, nil)
	resp, err := p.client.Do(req)

	// Hitung Waktu Respon
	responseTime := time.Since(startTime).Seconds() * 1000 // dalam ms

	node.mutex.Lock()
	defer node.mutex.Unlock()

	// Jika GAGAL (node mati, timeout, dll.)
	if err != nil {
		log.Printf("[METRIC] Gagal mengambil metrik dari %s: %v\n", node.Name, err)
		return
	}
	defer resp.Body.Close()

	// Jika SUKSES
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

// selectBackend_F_PSO_Framework kini menggunakan data real-time
func (p *NodePool) selectBackend_F_PSO_Framework() *Node {
	log.Println("[F-PSO] Memulai fase pengumpulan metrik real-time...")

	// 1. Fase Pengumpulan Metrik (Real-Time)
	var wg sync.WaitGroup
	for _, node := range p.nodes {
		wg.Add(1)
		go func(n *Node) {
			defer wg.Done()
			p.getRealNodeMetrics(n)
		}(node)
	}
	wg.Wait()
	log.Println("[F-PSO] Pengumpulan metrik selesai.")

	// 2. Fase Pemilihan (Placeholder: Pilih CPU Terendah)
	// --- DI SINILAH LOGIKA F-PSO ANDA AKAN DITEMPATKAN ---

	var bestNode *Node
	minCPU := math.MaxFloat64

	for _, node := range p.nodes {
		node.mutex.RLock()
		if node.CPUUsage < minCPU {
			minCPU = node.CPUUsage
			bestNode = node
		}
		node.mutex.RUnlock()
	}
	// --- AKHIR DARI LOGIKA F-PSO ---

	if bestNode == nil {
		log.Println("[F-PSO] Tidak ada node yang tersedia, kembali ke node pertama.")
		bestNode = p.nodes[0] // Fallback
	}

	// 3. Output Log
	log.Printf("[F-PSO] Metrik telah dihitung, dan F-PSO memilih Node: %s (CPU Terendah: %.2f%%)\n", bestNode.Name, minCPU)

	return bestNode
}

func newReverseProxy(pool *NodePool) *httputil.ReverseProxy {
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Memilih backend menggunakan kerangka F-PSO
			backendNode := pool.selectBackend_F_PSO_Framework()

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
	// --- PERUBAHAN DI SINI ---
	// Inisialisasi daftar Node HANYA 2 NODE
	backendDNS := []string{
		"http://api-node1:8080",
		"http://api-node2:8080",
	}
	// -------------------------

	// Buat HTTP client khusus untuk metrik
	metricsClient := &http.Client{
		Timeout: 1 * time.Second, // Timeout 3 detik
	}

	pool := &NodePool{client: metricsClient}
	for i, dns := range backendDNS {
		backendURL, err := url.Parse(dns)
		if err != nil {
			log.Fatalf("Gagal mem-parse URL backend: %v", err)
		}
		// --- PERUBAHAN DI SINI ---
		nodeName := fmt.Sprintf("api-node%d", i+1) // Tetap api-node1, api-node2
		// -------------------------

		pool.nodes = append(pool.nodes, &Node{
			Name: nodeName,
			URL:  backendURL,
		})
		log.Printf("Mendaftarkan backend node: %s di %s\n", nodeName, backendURL)
	}

	// Membuat reverse proxy
	proxy := newReverseProxy(pool)

	log.Println("Memulai Load Balancer F-PSO (Mode Real-Time) di port :8080...")
	// Jalankan server proxy
	if err := http.ListenAndServe(":8080", proxy); err != nil {
		log.Fatalf("Gagal memulai server proxy: %v", err)
	}
}
