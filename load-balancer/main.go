package main

import (
	"encoding/csv"
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
	"strconv"
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
	baseFuzzyParamsPath      = "configs/base_fuzzy_params.json"
	optimizedFuzzyParamsPath = "configs/optimized_fuzzy_params.json"
	psoParamsPath            = "storage/pso_params.json"
	fmopsoParamsPath         = "storage/fmopso_params.json"
	paretoFrontPath          = "storage/pareto_front.json"
	mainLogPath              = "logs/hasil_fpso.log"
	mopsoLogPath             = "logs/mopso_historical.log"
	trafficDatasetPath       = "logs/traffic_dataset.csv"
	osIdleCPU10              = 10.0
)

// Harus sama dengan yang ada di api-service.
type NodeMetrics struct {
	NodeName         string  `json:"node_name"`
	CPUUsage         float64 `json:"cpu_usage"`
	MemoryUsage      float64 `json:"memory_usage"`
	LoadAverage1     float64 `json:"load_average_1"`
	RequestLatencyMS float64 `json:"request_latency_ms"`
	InflightRequests float64 `json:"inflight_requests"`
	CPUCapacity      float64 `json:"cpu_capacity_percent"`
}

// backend service.
type Node struct {
	Name string
	URL  *url.URL

	CPUUsage     float64
	LoadAverage  float64
	InflightReq  float64
	MemoryUsage  float64
	ResponseTime float64
	CPUCapacity  float64

	RequestCount atomic.Int64
	mutex        sync.RWMutex
}

// NodePool menampung node-node backend.
type NodePool struct {
	nodes         []*Node
	client        *http.Client
	algorithm     string
	mopsoMode     string
	mopsoLogMu    sync.Mutex
	mopsoLog      *log.Logger
	trafficLogger *TrafficDatasetLogger
}

type TrafficDatasetLogger struct {
	path               string
	mode               string
	windowRequests     atomic.Int64
	windowCostMicroSum atomic.Int64
	writeMu            sync.Mutex
}

func NewTrafficDatasetLogger(path, mode string) *TrafficDatasetLogger {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		m = "window"
	}
	if m != "window" && m != "per_hit" {
		m = "window"
	}
	return &TrafficDatasetLogger{
		path: path,
		mode: m,
	}
}

func (t *TrafficDatasetLogger) Observe(costPerReq float64) {
	if costPerReq < 0 {
		costPerReq = 0
	}
	if t.mode == "per_hit" {
		if err := t.appendRow(time.Now(), 1, costPerReq); err != nil {
			log.Printf("[TRAFFIC-LOGGER] gagal append per-hit: %v", err)
		}
		return
	}
	t.windowRequests.Add(1)
	t.windowCostMicroSum.Add(int64(costPerReq * 1_000_000))
}

func (t *TrafficDatasetLogger) Start(interval time.Duration) {
	if err := t.ensureCSVHeader(); err != nil {
		log.Printf("[TRAFFIC-LOGGER] gagal menyiapkan CSV %s: %v", t.path, err)
		return
	}
	if t.mode == "per_hit" {
		log.Printf("[TRAFFIC-LOGGER] mode=per_hit, setiap DECISION ditulis langsung")
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for now := range ticker.C {
			req := t.windowRequests.Swap(0)
			costMicro := t.windowCostMicroSum.Swap(0)
			if req <= 0 {
				continue
			}
			avgCost := 0.0
			avgCost = float64(costMicro) / 1_000_000.0 / float64(req)
			if err := t.appendRow(now, req, avgCost); err != nil {
				log.Printf("[TRAFFIC-LOGGER] gagal append row: %v", err)
			}
		}
	}()
}

func (t *TrafficDatasetLogger) ensureCSVHeader() error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	content, err := os.ReadFile(t.path)
	if err == nil && len(content) > 0 {
		firstLine := string(content)
		if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
			firstLine = firstLine[:idx]
		}
		firstLine = strings.TrimSpace(strings.TrimPrefix(firstLine, "\ufeff"))
		if strings.EqualFold(firstLine, "Timestamp,TotalRequests,AvgCostPerReq") {
			return nil
		}
		// File lama tanpa header: prepend header agar kompatibel trainer.
		withHeader := append([]byte("Timestamp,TotalRequests,AvgCostPerReq\n"), content...)
		return os.WriteFile(t.path, withHeader, 0o666)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(t.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return err
	}
	defer file.Close()
	w := csv.NewWriter(file)
	if err := w.Write([]string{"Timestamp", "TotalRequests", "AvgCostPerReq"}); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

func (t *TrafficDatasetLogger) appendRow(ts time.Time, totalReq int64, avgCost float64) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	file, err := os.OpenFile(t.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return err
	}
	defer file.Close()
	w := csv.NewWriter(file)
	row := []string{
		ts.Format(time.RFC3339),
		strconv.FormatInt(totalReq, 10),
		strconv.FormatFloat(avgCost, 'f', 8, 64),
	}
	if err := w.Write(row); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
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

func nodeQueueSignal(n *Node) float64 {
	// Gabungkan in-flight request dengan load average host untuk sinyal queue yang lebih responsif.
	inflightSignal := n.InflightReq * 30.0
	loadSignal := n.LoadAverage * 10.0
	if inflightSignal > loadSignal {
		return inflightSignal
	}
	return loadSignal
}

func sanitizeFuzzyParams(params []float64) []float64 {
	out := append([]float64(nil), params...)
	const eps = 1e-6
	for i := 0; i+2 < len(out); i += 3 {
		hi := 2000.0
		if i <= 6 {
			hi = 100
		}
		a := math.Max(0, math.Min(hi, out[i]))
		b := math.Max(0, math.Min(hi, out[i+1]))
		c := math.Max(0, math.Min(hi, out[i+2]))

		if a > b {
			a, b = b, a
		}
		if b > c {
			b, c = c, b
		}
		if a > b {
			a, b = b, a
		}
		if b < a+eps {
			b = a + eps
		}
		if c < b+eps {
			c = b + eps
		}
		if c > hi {
			c = hi
			if b > c-eps {
				b = c - eps
			}
			if b < a+eps {
				a = math.Max(0, b-eps)
			}
		}

		out[i] = a
		out[i+1] = b
		out[i+2] = c
	}
	return out
}

func evaluateFitnessRealtime(params []float64, n1, n2 *Node) float64 {
	safeParams := sanitizeFuzzyParams(params)
	dummyEngine := fuzzy.NewEngine(safeParams)
	q1 := nodeQueueSignal(n1)
	q2 := nodeQueueSignal(n2)
	score1 := dummyEngine.CalculateMamdani(fuzzy.NodeMetrics{CPU: n1.CPUUsage, QueueLength: q1, RespTime: n1.ResponseTime}, myRules)
	score2 := dummyEngine.CalculateMamdani(fuzzy.NodeMetrics{CPU: n2.CPUUsage, QueueLength: q2, RespTime: n2.ResponseTime}, myRules)

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

	cap1 := math.Max(n1.CPUCapacity, 1)
	cap2 := math.Max(n2.CPUCapacity, 1)
	totalNormCPU := (n1.CPUUsage / cap1) + (n2.CPUUsage / cap2)
	if totalNormCPU < 1e-6 {
		totalNormCPU = 1e-6
	}
	util1 := totalNormCPU * share1
	util2 := totalNormCPU - util1

	di := math.Abs(util1-util2) / math.Max(util1+util2, 1e-9)
	bcu := math.Min(util1, util2) / math.Max(math.Max(util1, util2), 1e-9)
	peak := math.Max(util1, util2)
	latPenalty := math.Max(n1.ResponseTime, n2.ResponseTime) / 1000.0

	// Penalti imbalance dibuat non-linear agar PSO sangat alergi ke distribusi berat sebelah.
	balanceReward := 3.5*math.Pow(1.0-di, 3) + 2.5*math.Pow(bcu, 2)
	penalty := 1.30*peak + 0.45*latPenalty

	// Align reward: node dengan CPU lebih tinggi seharusnya mendapat skor fuzzy lebih rendah.
	align := 0.0
	if (n1.CPUUsage/cap1-n2.CPUUsage/cap2)*(score1-score2) < 0 {
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
	node.InflightReq = metrics.InflightRequests
	node.MemoryUsage = metrics.MemoryUsage
	if metrics.RequestLatencyMS > 0 {
		node.ResponseTime = metrics.RequestLatencyMS
	} else {
		node.ResponseTime = responseTime
	}
	if metrics.CPUCapacity > 0 {
		node.CPUCapacity = metrics.CPUCapacity
	} else {
		node.CPUCapacity = 100
	}

	cpuGauge.WithLabelValues(node.Name).Set(metrics.CPUUsage)
	latencyGauge.WithLabelValues(node.Name).Set(node.ResponseTime)

	log.Printf("[METRIC] Node %s: CPU=%.2f%%/%.0f%%, Load=%.2f, Inflight=%.2f, Latency=%.2fms\n", node.Name, node.CPUUsage, node.CPUCapacity, node.LoadAverage, node.InflightReq, node.ResponseTime)
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
		queue := nodeQueueSignal(node)
		lat := node.ResponseTime
		node.mutex.RUnlock()
		detailLog += fmt.Sprintf("[%s: CPU=%.2f%%, Q=%.2f, Lat=%.2fms -> Skor=0.0000] ", node.Name, cpu, queue, lat)
	}
	log.Printf("[DECISION] %s==> TERPILIH: %s\n", detailLog, selectedNode.Name)
	p.observeDecision(selectedNode)
	return selectedNode
}

func (p *NodePool) selectBackend_Fuzzy_Static() *Node {
	var bestNode *Node
	maxScore := -1.0
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		metrics := fuzzy.NodeMetrics{CPU: node.CPUUsage, QueueLength: nodeQueueSignal(node), RespTime: node.ResponseTime}
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
		p.observeDecision(bestNode)
	}
	return bestNode
}

func (p *NodePool) selectBackend_FPSO_Adaptive() *Node {
	var bestNode *Node
	maxScore := -1.0
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		metrics := fuzzy.NodeMetrics{CPU: node.CPUUsage, QueueLength: nodeQueueSignal(node), RespTime: node.ResponseTime}
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
		p.observeDecision(bestNode)
	}
	return bestNode
}

func (p *NodePool) selectBackend_FMOPSO_Adaptive() *Node {
	var bestNode *Node
	maxScore := -1.0
	var detailLog string

	for _, node := range p.nodes {
		node.mutex.RLock()
		metrics := fuzzy.NodeMetrics{CPU: node.CPUUsage, QueueLength: nodeQueueSignal(node), RespTime: node.ResponseTime}
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
		p.observeDecision(bestNode)
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
		log.Printf("[WARNING] ALGO tidak dikenal: %s. Fallback ke FUZZY.", p.algorithm)
		return p.selectBackend_Fuzzy_Static()
	}
}

func (p *NodePool) startPSOOptimizer(interval time.Duration) {
	if p.algorithm != "fpso" {
		log.Printf("[PSO-WORKER] Dilewati karena ALGO=%s (hanya aktif pada fpso)", p.algorithm)
		return
	}
	log.Printf("[PSO-WORKER] Optimizer F-PSO berjalan otomatis saat ada trafik...\n")
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			if len(p.nodes) < 2 {
				continue
			}
			now := time.Now().UnixNano()
			lastReq := lastRequestTime.Load()
			if (now - lastReq) > (3 * interval).Nanoseconds() {
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
			bestParams := sanitizeFuzzyParams(swarm.Optimize())

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
	if p.algorithm != "fmopso" {
		log.Printf("[MOPSO-WORKER] Dilewati karena ALGO=%s (hanya aktif pada fmopso)", p.algorithm)
		return
	}
	log.Printf("[MOPSO-WORKER] Optimizer F-MOPSO historical replay berjalan setiap %s\n", interval)
	go func() {
		ticker := time.NewTicker(interval)
		var prevReq1 int64
		var prevReq2 int64
		for range ticker.C {
			if len(p.nodes) < 2 {
				continue
			}

			curReq1 := p.nodes[0].RequestCount.Load()
			curReq2 := p.nodes[1].RequestCount.Load()
			r1 := curReq1 - prevReq1
			r2 := curReq2 - prevReq2
			if r1 < 0 {
				r1 = 0
			}
			if r2 < 0 {
				r2 = 0
			}
			prevReq1 = curReq1
			prevReq2 = curReq2
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
					CPUCapacity:  n1.CPUCapacity,
					QueueLength:  nodeQueueSignal(&n1),
					ResponseTime: n1.ResponseTime,
					Requests:     r1,
				},
				Node2: mopso.NodeState{
					CPUUsage:     n2.CPUUsage,
					CPUCapacity:  n2.CPUCapacity,
					QueueLength:  nodeQueueSignal(&n2),
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
			activeParams := sanitizeFuzzyParams(active.Solution.Params)
			AdaptiveFMOPSOEngine.UpdateParams(activeParams)
			if err := saveJSONToFile(fmopsoParamsPath, activeParams); err != nil {
				p.logMOPSO("[MOPSO] Gagal menyimpan parameter aktif ke %s: %v", fmopsoParamsPath, err)
			}
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

func (p *NodePool) observeDecision(node *Node) {
	if p.trafficLogger == nil || node == nil {
		return
	}
	node.mutex.RLock()
	costPerReq := node.ResponseTime
	node.mutex.RUnlock()
	if costPerReq <= 0 {
		costPerReq = 1
	}
	p.trafficLogger.Observe(costPerReq)
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

func envDurationMS(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	ms, err := time.ParseDuration(raw)
	if err == nil {
		return ms
	}
	if n, convErr := time.ParseDuration(raw + "ms"); convErr == nil {
		return n
	}
	return fallback
}

func resolveFuzzyConfig(activeAlgo string) string {
	switch strings.ToUpper(strings.TrimSpace(activeAlgo)) {
	case "FUZZY_MOPSO_OFFLINE":
		return optimizedFuzzyParamsPath
	case "FUZZY_MANUAL":
		return baseFuzzyParamsPath
	default:
		return baseFuzzyParamsPath
	}
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
	baseParams = sanitizeFuzzyParams(baseParams)

	activeAlgo := strings.TrimSpace(os.Getenv("ACTIVE_ALGO"))
	if activeAlgo == "" {
		activeAlgo = "FUZZY_MANUAL"
	}
	configPath := strings.TrimSpace(os.Getenv("FUZZY_CONFIG_PATH"))
	if configPath == "" {
		configPath = resolveFuzzyConfig(activeAlgo)
	}
	selectedParams := loadFloatArrayWithFallback(configPath, baseParams, "active fuzzy params")
	selectedParams = sanitizeFuzzyParams(selectedParams)
	StaticFuzzyEngine = fuzzy.NewEngine(selectedParams)
	log.Printf("[CONFIG] ACTIVE_ALGO=%s FUZZY_CONFIG_PATH=%s", activeAlgo, configPath)

	activeFMOPSOParams := loadFloatArrayWithFallback(fmopsoParamsPath, baseParams, "parameter adaptif F-MOPSO")
	activeFMOPSOParams = sanitizeFuzzyParams(activeFMOPSOParams)
	AdaptiveFMOPSOEngine = fuzzy.NewEngine(activeFMOPSOParams)

	activePSOParams := loadFloatArrayWithFallback(psoParamsPath, baseParams, "parameter adaptif F-PSO")
	activePSOParams = sanitizeFuzzyParams(activePSOParams)
	AdaptiveFPSOEngine = fuzzy.NewEngine(activePSOParams)

	backendDNS := []string{"http://api-node1:8080", "http://api-node2:8080"}
	metricsClient := &http.Client{Timeout: 500 * time.Millisecond}

	pool := &NodePool{
		client:    metricsClient,
		algorithm: envLower("LB_ALGO", "fuzzy"),
		mopsoMode: envLower("MOPSO_BUSINESS_MODE", "balanced"),
	}
	switch pool.algorithm {
	case "roundrobin", "fuzzy", "fmopso":
	default:
		log.Printf("[WARNING] LB_ALGO=%s tidak valid, fallback ke fuzzy", pool.algorithm)
		pool.algorithm = "fuzzy"
	}
	if mopsoFile != nil {
		pool.mopsoLog = log.New(mopsoFile, "", log.LstdFlags)
	}
	trafficLogMode := strings.ToLower(strings.TrimSpace(os.Getenv("TRAFFIC_LOG_MODE")))
	if trafficLogMode == "" {
		trafficLogMode = "window"
	}
	trafficLogInterval := envDurationMS("TRAFFIC_LOG_INTERVAL", 5*time.Second)
	pool.trafficLogger = NewTrafficDatasetLogger(trafficDatasetPath, trafficLogMode)
	pool.trafficLogger.Start(trafficLogInterval)
	log.Printf("[TRAFFIC-LOGGER] aktif -> %s (source=DECISION, mode=%s, interval=%s)", trafficDatasetPath, trafficLogMode, trafficLogInterval)

	for i, dns := range backendDNS {
		backendURL, err := url.Parse(dns)
		if err != nil {
			log.Fatalf("Gagal mem-parse URL backend: %v", err)
		}
		nodeName := fmt.Sprintf("api-node%d", i+1)
		pool.nodes = append(pool.nodes, &Node{Name: nodeName, URL: backendURL, CPUCapacity: 100})
		log.Printf("Mendaftarkan backend node: %s di %s\n", nodeName, backendURL)
	}

	metricsInterval := envDurationMS("METRICS_INTERVAL", 200*time.Millisecond)
	optimizerInterval := envDurationMS("OPTIMIZER_INTERVAL", 1*time.Second)
	pool.startMetricsCollector(metricsInterval)
	switch pool.algorithm {
	case "fmopso":
		pool.startMOPSOOptimizer(optimizerInterval)
	case "fuzzy", "roundrobin":
		log.Printf("[OPTIMIZER] Mode %s aktif, worker optimizer dilewati", pool.algorithm)
	default:
		log.Printf("[OPTIMIZER] ALGO=%s tidak didukung optimizer, worker dilewati", pool.algorithm)
	}

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

	log.Printf("Memulai Load Balancer di port :8080 (ALGO=%s, MOPSO_MODE=%s, METRICS_INTERVAL=%s, OPT_INTERVAL=%s)...", pool.algorithm, pool.mopsoMode, metricsInterval, optimizerInterval)
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
