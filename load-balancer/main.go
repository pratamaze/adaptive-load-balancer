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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"load-balancer/pkg/fuzzy"
	"load-balancer/pkg/mopso"
	"load-balancer/pkg/pso"
	"load-balancer/pkg/roundrobin"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var lastRequestTime atomic.Int64

const (
	baseFuzzyParamsPath = "configs/base_fuzzy_params.json"
	psoParamsPath       = "storage/pso_params.json"
	paretoFrontPath     = "storage/pareto_front.json"
	mainLogPath         = "logs/hasil_fpso.log"
	mopsoLogPath        = "logs/mopso_historical.log"
	osIdleCPU10         = 10.0
)

// Harus sama dengan yang ada di api-service.
type NodeMetrics struct {
	NodeName     string  `json:"node_name"`
	CPUUsage     float64 `json:"cpu_usage"`
	MemoryUsage  float64 `json:"memory_usage"`
	LoadAverage1 float64 `json:"load_average_1"`
}

// backend service.
type Node struct {
	Name string
	URL  *url.URL

	CPUUsage     float64
	LoadAverage  float64
	MemoryUsage  float64
	ResponseTime float64

	RequestCount atomic.Int64
	mutex        sync.RWMutex
}

// NodePool menampung node-node backend.
type NodePool struct {
	nodes      []*Node
	client     *http.Client
	algorithm  string
	mopsoMode  string
	mopsoLogMu sync.Mutex
	mopsoLog   *log.Logger
}

// 27 rules.
var myRules = []fuzzy.Rule{
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},

	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Rendah"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},

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

var DefaultBaseFuzzyParams = []float64{
	0, 0, 50, 0, 50, 100, 50, 100, 100,
	0, 0, 500, 0, 500, 1000, 500, 1000, 1000,
	0, 0, 500, 0, 500, 1000, 500, 1000, 1000,
}

var (
	StaticFuzzyEngine    = fuzzy.NewEngine(DefaultBaseFuzzyParams)
	AdaptiveFPSOEngine   = fuzzy.NewEngine(DefaultBaseFuzzyParams)
	AdaptiveFMOPSOEngine = fuzzy.NewEngine(DefaultBaseFuzzyParams)
)

func ensureDirs(paths ...string) {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			log.Fatalf("Gagal membuat direktori %s: %v", p, err)
		}
	}
}

func ensureBaseParamsFile(filename string, defaults []float64) {
	if _, err := os.Stat(filename); err == nil {
		return
	}
	if err := saveJSONToFile(filename, defaults); err != nil {
		log.Printf("[WARNING] Gagal membuat base params file %s: %v", filename, err)
	}
}

func loadFloatArrayWithFallback(filename string, fallback []float64, label string) []float64 {
	data, err := os.ReadFile(filename)
	if err != nil {
		log.Printf("[INFO] File %s tidak ditemukan. Menggunakan %s default.", filename, label)
		return append([]float64(nil), fallback...)
	}
	var out []float64
	if err := json.Unmarshal(data, &out); err != nil || len(out) != len(fallback) {
		log.Printf("[WARNING] Gagal membaca %s. Menggunakan %s default.", filename, label)
		return append([]float64(nil), fallback...)
	}
	log.Printf("[SUCCESS] Berhasil memuat %s dari %s", label, filename)
	return out
}

func saveJSONToFile(filename string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func evaluateFitnessRealtime(params []float64, n1, n2 *Node) float64 {
	dummyEngine := fuzzy.NewEngine(params)
	score1 := dummyEngine.CalculateMamdani(fuzzy.NodeMetrics{CPU: n1.CPUUsage, QueueLength: n1.LoadAverage * 10, RespTime: n1.ResponseTime}, myRules)
	score2 := dummyEngine.CalculateMamdani(fuzzy.NodeMetrics{CPU: n2.CPUUsage, QueueLength: n2.LoadAverage * 10, RespTime: n2.ResponseTime}, myRules)

	totalScore := score1 + score2
	share1 := 0.5
	if totalScore > 1e-9 {
		share1 = score1 / totalScore
		if share1 < 0 {
			share1 = 0
		}
		if share1 > 1 {
			share1 = 1
		}
	}

	totalCPU := n1.CPUUsage + n2.CPUUsage
	if totalCPU < 1 {
		totalCPU = 1
	}
	sim1 := totalCPU * share1
	sim2 := totalCPU - sim1

	di := math.Abs(sim1-sim2) / math.Max(sim1+sim2, 1e-9)
	bcu := math.Min(sim1, sim2) / math.Max(math.Max(sim1, sim2), 1e-9)
	peak := math.Max(sim1, sim2) / 100.0
	latPenalty := math.Max(n1.ResponseTime, n2.ResponseTime) / 1000.0

	// Penalti imbalance dibuat non-linear agar PSO sangat alergi ke distribusi berat sebelah.
	balanceReward := 3.5*math.Pow(1.0-di, 3) + 2.5*math.Pow(bcu, 2)
	penalty := 1.30*peak + 0.45*latPenalty

	// Align reward: node dengan CPU lebih tinggi seharusnya mendapat skor fuzzy lebih rendah.
	align := 0.0
	if (n1.CPUUsage-n2.CPUUsage)*(score1-score2) < 0 {
		align = 0.35
	}

	return balanceReward - penalty + align
}

func (p *NodePool) getRealNodeMetrics(node *Node) {
	metricsURL := node.URL.String() + "/metrics"
	startTime := time.Now()

	req, _ := http.NewRequest("GET", metricsURL, nil)
	resp, err := p.client.Do(req)
	responseTime := time.Since(startTime).Seconds() * 1000

	node.mutex.Lock()
	defer node.mutex.Unlock()

	if err != nil {
		log.Printf("[METRIC] Gagal mengambil metrik dari %s: %v\n", node.Name, err)
		node.CPUUsage = 100.0
		node.ResponseTime = 99999.0
		return
	}
	defer resp.Body.Close()

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

	node.CPUUsage = metrics.CPUUsage
	node.LoadAverage = metrics.LoadAverage1
	node.MemoryUsage = metrics.MemoryUsage
	node.ResponseTime = responseTime

	cpuGauge.WithLabelValues(node.Name).Set(metrics.CPUUsage)
	latencyGauge.WithLabelValues(node.Name).Set(responseTime)

	log.Printf("[METRIC] Node %s: CPU=%.2f%%, Load=%.2f, Latency=%.2fms\n", node.Name, node.CPUUsage, node.LoadAverage, node.ResponseTime)
}

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

var rrBalancer = roundrobin.New()

func (p *NodePool) selectBackend_RoundRobin() *Node {
	totalNodes := len(p.nodes)
	if totalNodes == 0 {
		return nil
	}
	idx := rrBalancer.NextIndex(totalNodes)
	selectedNode := p.nodes[idx]

	var detailLog string
	for _, node := range p.nodes {
		node.mutex.RLock()
		cpu := node.CPUUsage
		queue := node.LoadAverage * 10
		lat := node.ResponseTime
		node.mutex.RUnlock()
		detailLog += fmt.Sprintf("[%s: CPU=%.2f%%, Q=%.2f, Lat=%.2fms -> Skor=0.0000] ", node.Name, cpu, queue, lat)
	}
	log.Printf("[DECISION] %s==> TERPILIH: %s\n", detailLog, selectedNode.Name)
	return selectedNode
}

func (p *NodePool) selectBackend_Fuzzy_Static() *Node {
	var bestNode *Node
	maxScore := -1.0
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		metrics := fuzzy.NodeMetrics{CPU: node.CPUUsage, QueueLength: node.LoadAverage * 10, RespTime: node.ResponseTime}
		node.mutex.RUnlock()

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

func (p *NodePool) selectBackend_FPSO_Adaptive() *Node {
	var bestNode *Node
	maxScore := -1.0
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		metrics := fuzzy.NodeMetrics{CPU: node.CPUUsage, QueueLength: node.LoadAverage * 10, RespTime: node.ResponseTime}
		node.mutex.RUnlock()

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

func (p *NodePool) selectBackend_FMOPSO_Adaptive() *Node {
	var bestNode *Node
	maxScore := -1.0
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		metrics := fuzzy.NodeMetrics{CPU: node.CPUUsage, QueueLength: node.LoadAverage * 10, RespTime: node.ResponseTime}
		node.mutex.RUnlock()

		score := AdaptiveFMOPSOEngine.CalculateMamdani(metrics, myRules)
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
		log.Printf("[DECISION] %s ==> TERPILIH (F-MOPSO): %s\n", detailLog, bestNode.Name)
	}
	return bestNode
}

func (p *NodePool) selectBackend() *Node {
	switch p.algorithm {
	case "roundrobin":
		return p.selectBackend_RoundRobin()
	case "fuzzy":
		return p.selectBackend_Fuzzy_Static()
	case "fmopso":
		return p.selectBackend_FMOPSO_Adaptive()
	default:
		return p.selectBackend_FPSO_Adaptive()
	}
}

func (p *NodePool) startPSOOptimizer(interval time.Duration) {
	log.Printf("[PSO-WORKER] Optimizer F-PSO berjalan otomatis saat ada trafik...\n")
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			if len(p.nodes) < 2 {
				continue
			}
			now := time.Now().UnixNano()
			lastReq := lastRequestTime.Load()
			if (now - lastReq) > (5 * time.Second).Nanoseconds() {
				continue
			}

			p.nodes[0].mutex.RLock()
			n1 := *p.nodes[0]
			p.nodes[0].mutex.RUnlock()
			p.nodes[1].mutex.RLock()
			n2 := *p.nodes[1]
			p.nodes[1].mutex.RUnlock()

			currentParams := AdaptiveFPSOEngine.GetParams()
			swarm := pso.NewSwarm(currentParams, func(params []float64) float64 {
				return evaluateFitnessRealtime(params, &n1, &n2)
			})
			bestParams := swarm.Optimize()

			AdaptiveFPSOEngine.UpdateParams(bestParams)
			if err := saveJSONToFile(psoParamsPath, bestParams); err != nil {
				log.Printf("[PSO] Gagal menyimpan parameter ke %s: %v", psoParamsPath, err)
			} else {
				log.Printf("[PSO] Model beradaptasi dari trafik masuk dan disimpan ke %s", psoParamsPath)
			}
		}
	}()
}

func (p *NodePool) startMOPSOOptimizer(interval time.Duration) {
	log.Printf("[MOPSO-WORKER] Optimizer F-MOPSO historical replay berjalan setiap %s\n", interval)
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			if len(p.nodes) < 2 {
				continue
			}

			r1 := p.nodes[0].RequestCount.Swap(0)
			r2 := p.nodes[1].RequestCount.Swap(0)
			totalReq := r1 + r2
			if totalReq == 0 {
				p.logMOPSO("[FACT] Total Request=0, replay dilewati untuk hemat CPU")
				continue
			}

			p.nodes[0].mutex.RLock()
			n1 := *p.nodes[0]
			p.nodes[0].mutex.RUnlock()
			p.nodes[1].mutex.RLock()
			n2 := *p.nodes[1]
			p.nodes[1].mutex.RUnlock()

			snapshot := mopso.HistoricalSnapshot{
				OSIdleCPU10: osIdleCPU10,
				Node1: mopso.NodeState{
					CPUUsage:     n1.CPUUsage,
					QueueLength:  n1.LoadAverage * 10,
					ResponseTime: n1.ResponseTime,
					Requests:     r1,
				},
				Node2: mopso.NodeState{
					CPUUsage:     n2.CPUUsage,
					QueueLength:  n2.LoadAverage * 10,
					ResponseTime: n2.ResponseTime,
					Requests:     r2,
				},
			}

			base := AdaptiveFMOPSOEngine.GetParams()
			result := mopso.OptimizeReplay(base, snapshot)
			if len(result.Archive) == 0 || len(result.Compromises) == 0 {
				p.logMOPSO("[MOPSO] Pareto archive kosong, update parameter dilewati")
				continue
			}

			active, ok := mopso.ActiveByMode(result, p.mopsoMode)
			if !ok {
				p.logMOPSO("[MOPSO] Tidak ada solusi aktif")
				continue
			}
			AdaptiveFMOPSOEngine.UpdateParams(active.Solution.Params)
			mopsoCostPerRequestGauge.WithLabelValues(n1.Name).Set(result.CostPerReq1)
			mopsoCostPerRequestGauge.WithLabelValues(n2.Name).Set(result.CostPerReq2)
			mopsoParetoArchiveGauge.Set(float64(len(result.Archive)))
			mopsoFitnessScoreGauge.WithLabelValues("imbalance").Set(active.Solution.Objective.Imbalance)
			mopsoFitnessScoreGauge.WithLabelValues("peak_load").Set(active.Solution.Objective.PeakLoad)
			if p.mopsoMode == "performance" {
				mopsoActiveModeGauge.WithLabelValues("performance").Set(1)
				mopsoActiveModeGauge.WithLabelValues("balanced").Set(0)
			} else {
				mopsoActiveModeGauge.WithLabelValues("balanced").Set(1)
				mopsoActiveModeGauge.WithLabelValues("performance").Set(0)
			}

			payload := struct {
				GeneratedAt string             `json:"generated_at"`
				Mode        string             `json:"active_business_mode"`
				Active      mopso.Compromise   `json:"active_solution"`
				Result      mopso.ParetoResult `json:"result"`
			}{
				GeneratedAt: time.Now().Format(time.RFC3339),
				Mode:        p.mopsoMode,
				Active:      active,
				Result:      result,
			}
			if err := saveJSONToFile(paretoFrontPath, payload); err != nil {
				p.logMOPSO("[MOPSO] Gagal menyimpan Pareto archive: %v", err)
			}

			p.logMOPSO("[FACT] Total Request=%d, Pembagian=[%s:%d, %s:%d], CPU Aktual=[%s:%.2f%%, %s:%.2f%%]", totalReq, n1.Name, r1, n2.Name, r2, n1.Name, n1.CPUUsage, n2.Name, n2.CPUUsage)
			p.logMOPSO("[MATH] CostPerReq=(ActualCPU-Idle10)/max(req,1) => [%s:%.8f, %s:%.8f]", n1.Name, result.CostPerReq1, n2.Name, result.CostPerReq2)
			p.logMOPSO("[MOPSO] ParetoSolutions=%d, ActiveMode=%s, ActiveProfile=%s, ActiveObjectives=(f1=%.6f,f2=%.6f)", len(result.Archive), p.mopsoMode, active.Mode, active.Solution.Objective.Imbalance, active.Solution.Objective.PeakLoad)
		}
	}()
}

func (p *NodePool) logMOPSO(format string, args ...any) {
	if p.mopsoLog == nil {
		return
	}
	p.mopsoLogMu.Lock()
	defer p.mopsoLogMu.Unlock()
	p.mopsoLog.Printf(format, args...)
}

func newReverseProxy(pool *NodePool) *httputil.ReverseProxy {
	customTransport := &http.Transport{
		MaxIdleConns:          10000,
		MaxIdleConnsPerHost:   5000,
		MaxConnsPerHost:       15000,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	proxy := &httputil.ReverseProxy{
		Transport: customTransport,
		Director: func(req *http.Request) {
			lastRequestTime.Store(time.Now().UnixNano())
			backendNode := pool.selectBackend()
			if backendNode == nil {
				log.Println("Gagal memilih backend, tidak ada node tersedia.")
				return
			}

			backendNode.RequestCount.Add(1)
			req.URL.Scheme = backendNode.URL.Scheme
			req.URL.Host = backendNode.URL.Host
			req.Host = backendNode.URL.Host
		},
		ModifyResponse: func(res *http.Response) error { return nil },
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("Gagal meneruskan request ke backend: %v\n", err)
			http.Error(w, "Service tidak tersedia", http.StatusServiceUnavailable)
		},
	}
	return proxy
}

var (
	cpuGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "pso_node_cpu_usage", Help: "Penggunaan CPU node backend (%)"},
		[]string{"node_name"},
	)
	latencyGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "pso_node_latency_ms", Help: "Latensi komunikasi ke node (ms)"},
		[]string{"node_name"},
	)
	mopsoCostPerRequestGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "mopso_cost_per_request", Help: "Estimasi biaya CPU per request dari historical replay"},
		[]string{"node_name"},
	)
	mopsoActiveModeGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "mopso_active_mode", Help: "Mode bisnis MOPSO aktif (1=aktif,0=nonaktif)"},
		[]string{"mode"},
	)
	mopsoFitnessScoreGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "mopso_fitness_score", Help: "Skor objective aktif dari solusi MOPSO"},
		[]string{"objective"},
	)
	mopsoParetoArchiveGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "mopso_pareto_archive_size", Help: "Jumlah solusi non-dominated dalam Pareto archive"},
	)
)

func init() {
	prometheus.MustRegister(cpuGauge)
	prometheus.MustRegister(latencyGauge)
	prometheus.MustRegister(mopsoCostPerRequestGauge)
	prometheus.MustRegister(mopsoActiveModeGauge)
	prometheus.MustRegister(mopsoFitnessScoreGauge)
	prometheus.MustRegister(mopsoParetoArchiveGauge)
}

func envLower(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return strings.ToLower(v)
}

func main() {
	ensureDirs("configs", "storage", "logs")

	logFile, err := os.OpenFile(mainLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		log.Printf("Gagal membuka file log di %s: %v. Log hanya tampil di terminal.", mainLogPath, err)
	} else {
		defer logFile.Close()
		log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	}

	mopsoFile, err := os.OpenFile(mopsoLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		log.Printf("[WARNING] Gagal membuka log MOPSO %s: %v", mopsoLogPath, err)
	}
	defer func() {
		if mopsoFile != nil {
			_ = mopsoFile.Close()
		}
	}()

	ensureBaseParamsFile(baseFuzzyParamsPath, DefaultBaseFuzzyParams)
	baseParams := loadFloatArrayWithFallback(baseFuzzyParamsPath, DefaultBaseFuzzyParams, "base fuzzy params")

	StaticFuzzyEngine = fuzzy.NewEngine(baseParams)
	AdaptiveFMOPSOEngine = fuzzy.NewEngine(baseParams)

	activePSOParams := loadFloatArrayWithFallback(psoParamsPath, baseParams, "parameter adaptif F-PSO")
	AdaptiveFPSOEngine = fuzzy.NewEngine(activePSOParams)

	backendDNS := []string{"http://api-node1:8080", "http://api-node2:8080"}
	metricsClient := &http.Client{Timeout: 500 * time.Millisecond}

	pool := &NodePool{
		client:    metricsClient,
		algorithm: envLower("LB_ALGO", "fpso"),
		mopsoMode: envLower("MOPSO_BUSINESS_MODE", "balanced"),
	}
	if mopsoFile != nil {
		pool.mopsoLog = log.New(mopsoFile, "", log.LstdFlags)
	}

	for i, dns := range backendDNS {
		backendURL, err := url.Parse(dns)
		if err != nil {
			log.Fatalf("Gagal mem-parse URL backend: %v", err)
		}
		nodeName := fmt.Sprintf("api-node%d", i+1)
		pool.nodes = append(pool.nodes, &Node{Name: nodeName, URL: backendURL})
		log.Printf("Mendaftarkan backend node: %s di %s\n", nodeName, backendURL)
	}

	pool.startMetricsCollector(200 * time.Millisecond)
	pool.startPSOOptimizer(5 * time.Second)
	pool.startMOPSOOptimizer(5 * time.Second)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	proxy := newReverseProxy(pool)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			promhttp.Handler().ServeHTTP(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	log.Printf("Memulai Load Balancer di port :8080 (ALGO=%s, MOPSO_MODE=%s)...", pool.algorithm, pool.mopsoMode)
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
