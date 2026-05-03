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
	"load-balancer/pkg/roundrobin"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

/**
Package main mengimplementasikan entry-point load balancer adaptif untuk tiga mode:
1) roundrobin: distribusi giliran sederhana.
2) fuzzy: seleksi node berbasis fuzzy Mamdani dengan parameter statis.
3) fmopso: seleksi fuzzy adaptif, parameter dioptimasi realtime via replay historis.

File ini sengaja disusun mengikuti urutan alur operasional:
configuration -> model -> utility -> metrics -> decision -> optimizer -> http -> bootstrap.
Tujuannya agar proses audit teknis dan audit matematis dapat diikuti dari atas ke bawah.
*/

const (
	// Path runtime relatif terhadap working directory container (`/` pada image final).
	baseFuzzyParamsPath = "configs/base_fuzzy_params.json"
	optimizedFuzzyPath  = "configs/optimized_fuzzy_params.json"
	fmopsoParamsPath    = "storage/fmopso_params.json"
	paretoFrontPath     = "storage/pareto_front.json"
	// Asumsi baseline idle CPU host (dalam persen) untuk model replay FMOPSO.
	osIdleCPU10 = 10.0
)

/*
*
NodeMetrics adalah kontrak payload metrik dari backend (`api-service`).
Struktur ini harus sinkron dengan endpoint `/metrics` di service backend.
*/
type NodeMetrics struct {
	NodeName           string  `json:"node_name"`
	CPUUsage           float64 `json:"cpu_usage"`
	CPUUsageRaw        float64 `json:"cpu_usage_raw"`
	CPUUsageNormalized float64 `json:"cpu_usage_normalized"`
	MemoryUsage        float64 `json:"memory_usage"`
	LoadAverage1       float64 `json:"load_average_1"`
	RequestLatencyMS   float64 `json:"request_latency_ms"`
	InflightRequests   float64 `json:"inflight_requests"`
	CPUCapacity        float64 `json:"cpu_capacity_percent"`
}

/*
*
Node menyimpan URL backend dan snapshot metrik terakhir yang digunakan oleh selector.
Semua update metrik dilakukan secara thread-safe via mutex.
*/
type Node struct {
	Name string
	URL  *url.URL

	CPUUsage     float64
	CPURawUsage  float64
	LoadAverage  float64
	InflightReq  float64
	MemoryUsage  float64
	ResponseTime float64
	CPUCapacity  float64

	RequestCount atomic.Int64
	mutex        sync.RWMutex
}

/*
*
NodeDecisionSnapshot adalah snapshot immutable metrik node pada satu titik waktu.
Tujuan utama:
1) menghindari race saat proses decision (metrik dibaca sekali, lalu dipakai berulang),
2) memudahkan audit manual karena nilai input fuzzy terlihat eksplisit dan konsisten,
3) mencegah bug lock yang salah objek (misalnya lock node A tapi membaca node B).

Field Queue adalah sinyal antrean gabungan hasil fungsi `nodeQueueSignal`.
*/
type NodeDecisionSnapshot struct {
	Node     *Node
	Name     string
	CPU      float64
	Queue    float64
	RespMS   float64
	CPURaw   float64
	CPUCap   float64
	Inflight float64
	LoadAvg  float64
}

/*
*
NodePool adalah runtime state load balancer:
- daftar node backend,
- konfigurasi algoritma aktif,
- mode bisnis FMOPSO,
- mode logging dataset fuzzy.
*/
type NodePool struct {
	nodes          []*Node
	client         *http.Client
	algorithm      string
	mopsoMode      string
	paramProfile   string
	trafficLogMode string
}

/*
*
RuntimeConfig mengenkapsulasi seluruh konfigurasi dari environment variable.
Dengan pendekatan ini, fungsi main tetap ringkas dan berfokus pada orkestrasi.
*/
type RuntimeConfig struct {
	Algorithm         string
	MOPSOMode         string
	ParamSource       string
	TrafficLogMode    string
	MetricsInterval   time.Duration
	OptimizerInterval time.Duration
	BackendDNS        []string
}

/*
*
FuzzyBootstrap merangkum hasil bootstrap parameter dan engine fuzzy:
- StaticParams untuk mode fuzzy,
- FMOPSOParams untuk mode fmopso,
- metadata profil parameter aktif untuk logging startup.
*/
type FuzzyBootstrap struct {
	StaticParams     []float64
	FMOPSOParams     []float64
	ParamProfile     string
	ActiveStaticFile string
}

const selectedNodeTraceHeader = "X-LB-Selected-Node"

/*
*
Rule base Mamdani 3x3x3:
input  = CPU x Queue x ResponseTime (masing-masing Low/Medium/High)
output = prioritas pemilihan node (Rendah/Sedang/Tinggi).
*/
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
	// StaticFuzzyEngine dipakai saat mode `fuzzy`.
	StaticFuzzyEngine = fuzzy.NewEngine(DefaultBaseFuzzyParams)
	// AdaptiveFMOPSOEngine dipakai saat mode `fmopso` dan bisa di-update realtime.
	AdaptiveFMOPSOEngine = fuzzy.NewEngine(DefaultBaseFuzzyParams)
)

// ensureDirs memastikan direktori runtime minimum selalu tersedia.
func ensureDirs(paths ...string) {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			log.Fatalf("Gagal membuat direktori %s: %v", p, err)
		}
	}
}

// ensureBaseParamsFile membuat file base params hanya jika belum ada.
func ensureBaseParamsFile(filename string, defaults []float64) {
	if _, err := os.Stat(filename); err == nil {
		return
	}
	if err := saveJSONToFile(filename, defaults); err != nil {
		log.Printf("[WARNING] Gagal membuat base params file %s: %v", filename, err)
	}
}

// loadFloatArrayWithFallback membaca file parameter dan fallback ke default bila gagal.
func loadFloatArrayWithFallback(filename string, fallback []float64, label string) []float64 {
	data, err := os.ReadFile(filename)
	if err != nil {
		log.Printf("[WARNING] File %s tidak ditemukan. Menggunakan %s default.", filename, label)
		return append([]float64(nil), fallback...)
	}
	var out []float64
	if err := json.Unmarshal(data, &out); err != nil || len(out) != len(fallback) {
		log.Printf("[WARNING] Gagal membaca %s. Menggunakan %s default.", filename, label)
		return append([]float64(nil), fallback...)
	}
	return out
}

// saveJSONToFile menyimpan payload JSON pretty-printed dan membuat parent dir jika perlu.
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

// nodeQueueSignal menurunkan dua sinyal antrean (inflight vs load average) menjadi satu skor queue.
// Nilai maksimum dipilih agar cepat merespons bottleneck yang paling dominan.
func nodeQueueSignal(n *Node) float64 {
	// Gabungkan in-flight request dengan load average host untuk sinyal queue yang lebih responsif.
	inflightSignal := n.InflightReq * 30.0
	loadSignal := n.LoadAverage * 10.0
	if inflightSignal > loadSignal {
		return inflightSignal
	}
	return loadSignal
}

// snapshotNodeForDecision membaca metrik node sekali lalu mengunci keputusan pada snapshot immutable.
func snapshotNodeForDecision(node *Node) NodeDecisionSnapshot {
	node.mutex.RLock()
	defer node.mutex.RUnlock()
	return NodeDecisionSnapshot{
		Node:     node,
		Name:     node.Name,
		CPU:      node.CPUUsage,
		Queue:    nodeQueueSignal(node),
		RespMS:   node.ResponseTime,
		CPURaw:   node.CPURawUsage,
		CPUCap:   node.CPUCapacity,
		Inflight: node.InflightReq,
		LoadAvg:  node.LoadAverage,
	}
}

// applyMetricPenalty memberi penalti berat ketika metrik node gagal diambil.
func applyMetricPenalty(node *Node) {
	node.CPUUsage = 100.0
	if node.CPUCapacity > 0 {
		node.CPURawUsage = node.CPUCapacity
	} else {
		node.CPURawUsage = 100.0
	}
	node.ResponseTime = 99999.0
}

// sanitizeFuzzyParams memaksa 27 parameter ke domain valid dan urutan segitiga a<=b<=c.
// CPU dibatasi [0..100], queue/resp dibatasi [0..2000].
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

// activeParamProfile menormalisasi profil parameter aktif agar log konsisten.
func (p *NodePool) activeParamProfile() string {
	profile := strings.TrimSpace(p.paramProfile)
	if profile == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(profile)
}

// getRealNodeMetrics mengambil metrik dari backend (`/metrics`) lalu menyimpannya ke snapshot node.
// Pada error, node diberi penalti berat agar kecil kemungkinan dipilih.
func (p *NodePool) getRealNodeMetrics(node *Node) {
	metricsURL := node.URL.String() + "/metrics"
	startTime := time.Now()

	resp, err := p.client.Get(metricsURL)
	responseTime := time.Since(startTime).Seconds() * 1000

	node.mutex.Lock()
	defer node.mutex.Unlock()

	if err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal mengambil metrik dari %s: %v\n", p.activeParamProfile(), node.Name, err)
		applyMetricPenalty(node)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal membaca body dari %s: %v\n", p.activeParamProfile(), node.Name, err)
		applyMetricPenalty(node)
		return
	}

	var metrics NodeMetrics
	if err := json.Unmarshal(body, &metrics); err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal parse JSON dari %s: %v\n", p.activeParamProfile(), node.Name, err)
		applyMetricPenalty(node)
		return
	}

	normalizedCPU := metrics.CPUUsage
	if metrics.CPUUsageNormalized > 0 || metrics.CPUUsage == 0 {
		normalizedCPU = metrics.CPUUsageNormalized
	}
	if normalizedCPU < 0 {
		normalizedCPU = 0
	}
	if normalizedCPU > 100 {
		normalizedCPU = 100
	}

	rawCPU := metrics.CPUUsageRaw
	if rawCPU <= 0 && metrics.CPUCapacity > 0 {
		rawCPU = normalizedCPU * (metrics.CPUCapacity / 100.0)
	}

	node.CPUUsage = normalizedCPU
	node.CPURawUsage = rawCPU
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

	cpuGauge.WithLabelValues(node.Name).Set(normalizedCPU)
	cpuRawGauge.WithLabelValues(node.Name).Set(rawCPU)
	latencyGauge.WithLabelValues(node.Name).Set(node.ResponseTime)
}

// updateAllMetrics melakukan refresh metrik seluruh node secara paralel.
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

// startMetricsCollector menjalankan loop koleksi metrik periodik.
func (p *NodePool) startMetricsCollector(interval time.Duration) {
	p.updateAllMetrics()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			p.updateAllMetrics()
		}
	}()
}

var rrBalancer = roundrobin.New()

// selectBackend_RoundRobin memilih node berdasarkan rotasi indeks atomik.
func (p *NodePool) selectBackend_RoundRobin() *Node {
	totalNodes := len(p.nodes)
	if totalNodes == 0 {
		return nil
	}
	idx := rrBalancer.NextIndex(totalNodes)
	selectedNode := p.nodes[idx]

	var detailLog string
	for _, node := range p.nodes {
		snap := snapshotNodeForDecision(node)
		detailLog += fmt.Sprintf("[%s: CPU=%.2f%%, Q=%.2f, Lat=%.2fms -> Skor=0.0000] ", snap.Name, snap.CPU, snap.Queue, snap.RespMS)
	}
	log.Printf("[DECISION][SRC=%s] %s==> TERPILIH: %s\n", p.activeParamProfile(), detailLog, selectedNode.Name)
	return selectedNode
}

// selectBackendByFuzzyEngine adalah jalur umum seleksi fuzzy.
// Engine disuntikkan agar bisa dipakai ulang untuk mode fuzzy statis dan FMOPSO adaptif.
func (p *NodePool) selectBackendByFuzzyEngine(engine *fuzzy.Engine, decisionTag string) *Node {
	/**
	Prosedur audit keputusan fuzzy (satu request):
	1) Ambil snapshot immutable tiap node -> (CPU, Queue, Resp) tidak berubah selama evaluasi request ini.
	2) Hitung skor Mamdani per node menggunakan rule base yang sama.
	3) Pilih skor terbesar sebagai kandidat utama.
	4) Jika skor sama persis (tie), gunakan tie-break berurutan:
	   a. CPU lebih kecil menang (mengurangi risiko overload jangka pendek),
	   b. jika CPU sama, latency lebih kecil menang (mengurangi tail-latency),
	   c. jika tetap sama, urutan nama node (lexicographic) sebagai deterministic fallback.

	Pola ini penting untuk audit manual karena setiap keputusan dapat direkonstruksi
	dari satu baris log DECISION tanpa ketergantungan urutan goroutine.
	*/
	var bestNode *NodeDecisionSnapshot
	maxScore := -1.0
	var detailLog string

	for _, node := range p.nodes {
		snap := snapshotNodeForDecision(node)
		metrics := fuzzy.NodeMetrics{
			CPU:         snap.CPU,
			QueueLength: snap.Queue,
			RespTime:    snap.RespMS,
		}

		score := engine.CalculateMamdani(metrics, myRules)
		detailLog += fmt.Sprintf("[%s: CPU=%.2f%%, Q=%.2f, Lat=%.2fms -> Skor=%.4f] ", snap.Name, metrics.CPU, metrics.QueueLength, metrics.RespTime, score)

		if score > maxScore {
			maxScore = score
			s := snap
			bestNode = &s
		} else if score == maxScore && bestNode != nil {
			if snap.CPU < bestNode.CPU ||
				(snap.CPU == bestNode.CPU && snap.RespMS < bestNode.RespMS) ||
				(snap.CPU == bestNode.CPU && snap.RespMS == bestNode.RespMS && snap.Name < bestNode.Name) {
				s := snap
				bestNode = &s
			}
		}
	}
	if bestNode != nil {
		log.Printf("[DECISION][SRC=%s] %s ==> TERPILIH (%s): %s\n", p.activeParamProfile(), detailLog, decisionTag, bestNode.Name)
		return bestNode.Node
	}
	return nil
}

// selectBackend_Fuzzy_Static memakai parameter fuzzy statis (base/optimized).
func (p *NodePool) selectBackend_Fuzzy_Static() *Node {
	return p.selectBackendByFuzzyEngine(StaticFuzzyEngine, "FUZZY")
}

// selectBackend_FMOPSO_Adaptive memakai parameter fuzzy yang di-update optimizer FMOPSO.
func (p *NodePool) selectBackend_FMOPSO_Adaptive() *Node {
	return p.selectBackendByFuzzyEngine(AdaptiveFMOPSOEngine, "F-MOPSO")
}

// selectBackend adalah dispatcher utama algoritma load balancing.
func (p *NodePool) selectBackend() *Node {
	switch p.algorithm {
	case "roundrobin":
		return p.selectBackend_RoundRobin()
	case "fuzzy":
		return p.selectBackend_Fuzzy_Static()
	case "fmopso":
		return p.selectBackend_FMOPSO_Adaptive()
	default:
		return p.selectBackend_Fuzzy_Static()
	}
}

/*
*
*
startMOPSOOptimizer menjalankan loop adaptasi parameter fuzzy untuk mode `fmopso`.

Rangkaian per interval (misal tiap 1 detik):
 1. Request window:
    hitung request baru pada node1 & node2 sejak interval sebelumnya.
    Nilai ini menjadi "traffic reality" yang akan direplay.
 2. Runtime snapshot:
    ambil snapshot metrik node (CPU raw, kapasitas CPU, queue signal, response time).
    Snapshot ini dianggap immutable untuk satu siklus optimasi agar evaluasi konsisten.
 3. Historical replay optimization:
    panggil `mopso.OptimizeReplay(baseParams, snapshot)` untuk menghasilkan Pareto archive.
    Setiap kandidat parameter memiliki dua objective minimization:
    - f1: imbalance penalty
    - f2: peak load penalty
 4. Business compromise activation:
    pilih solusi aktif via mode bisnis:
    - balanced     -> fokus keseimbangan node
    - performance  -> fokus peak-load minimum
 5. Activation + persistence:
    - sanitize parameter agar valid untuk domain membership function,
    - update engine fuzzy adaptif,
    - simpan parameter aktif ke file,
    - simpan artefak pareto untuk audit offline.

Catatan audit:
- Optimizer tidak mengubah rule base fuzzy, hanya parameter membership (27 dimensi).
- Semua langkah penting dicatat dengan log ringkas agar jejak perhitungan bisa ditelusuri manual.
*/
func (p *NodePool) startMOPSOOptimizer(interval time.Duration) {
	if p.algorithm != "fmopso" || len(p.nodes) < 2 {
		return
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var prevReq1 int64
		var prevReq2 int64

		for range ticker.C {
			r1, r2, totalReq := p.nextRequestWindow(&prevReq1, &prevReq2)
			if totalReq == 0 {
				continue
			}

			n1, n2 := p.snapshotNodePair()
			snapshot := buildReplaySnapshot(n1, n2, r1, r2)

			base := AdaptiveFMOPSOEngine.GetParams()
			result := mopso.OptimizeReplay(base, snapshot)
			if len(result.Archive) == 0 || len(result.Compromises) == 0 {
				continue
			}

			active, ok := mopso.ActiveByMode(result, p.mopsoMode)
			if !ok {
				continue
			}
			activeParams := sanitizeFuzzyParams(active.Solution.Params)
			AdaptiveFMOPSOEngine.UpdateParams(activeParams)
			if err := saveJSONToFile(fmopsoParamsPath, activeParams); err != nil {
				log.Printf("[WARNING] Gagal menyimpan parameter aktif ke %s: %v", fmopsoParamsPath, err)
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
				log.Printf("[WARNING] Gagal menyimpan Pareto archive: %v", err)
			}

			log.Printf(
				"[OPTIMIZER][SRC=%s][MODE=%s] req(%s=%d,%s=%d,total=%d) cost(%s=%.6f,%s=%.6f) active(f1=%.6f,f2=%.6f)",
				p.activeParamProfile(),
				p.mopsoMode,
				n1.Name, r1,
				n2.Name, r2,
				totalReq,
				n1.Name, result.CostPerReq1,
				n2.Name, result.CostPerReq2,
				active.Solution.Objective.Imbalance,
				active.Solution.Objective.PeakLoad,
			)
		}
	}()
}

/*
*
nextRequestWindow menghitung delta request per interval.

Konsep:
  - RequestCount di tiap node bersifat counter monotonik (atomic).
  - Untuk memperoleh request "pada interval ini", kita hitung:
    delta = current_counter - previous_counter
  - previous_counter kemudian digeser menjadi current_counter untuk interval berikutnya.

Proteksi audit:
  - Bila terjadi kondisi anomali (counter turun karena restart/reset), delta negatif dipaksa menjadi 0
    agar tidak mencemari input replay optimizer.
*/
func (p *NodePool) nextRequestWindow(prevReq1, prevReq2 *int64) (r1 int64, r2 int64, total int64) {
	curReq1 := p.nodes[0].RequestCount.Load()
	curReq2 := p.nodes[1].RequestCount.Load()
	r1 = curReq1 - *prevReq1
	r2 = curReq2 - *prevReq2
	if r1 < 0 {
		r1 = 0
	}
	if r2 < 0 {
		r2 = 0
	}
	*prevReq1 = curReq1
	*prevReq2 = curReq2
	return r1, r2, r1 + r2
}

/** snapshotNodePair membuat snapshot immutable dari dua node backend. */
func (p *NodePool) snapshotNodePair() (Node, Node) {
	p.nodes[0].mutex.RLock()
	n1 := *p.nodes[0]
	p.nodes[0].mutex.RUnlock()
	p.nodes[1].mutex.RLock()
	n2 := *p.nodes[1]
	p.nodes[1].mutex.RUnlock()
	return n1, n2
}

/*
*
buildReplaySnapshot mengubah snapshot runtime node menjadi format input replay FMOPSO.

Mapping audit yang perlu diperhatikan:
  - CPUUsage pada replay memakai `CPURawUsage` (bukan CPU normalized), karena evaluator replay
    menghitung estimasi biaya CPU/request pada domain absolut host.
  - QueueLength memakai `nodeQueueSignal` = max(inflight*30, loadAvg*10).
  - Requests berasal dari delta request interval berjalan (`r1`,`r2`), bukan total kumulatif.
  - OSIdleCPU10 adalah baseline idle CPU yang dipakai model replay untuk menghindari biaya/request negatif.
*/
func buildReplaySnapshot(n1, n2 Node, r1, r2 int64) mopso.HistoricalSnapshot {
	return mopso.HistoricalSnapshot{
		OSIdleCPU10: osIdleCPU10,
		Node1: mopso.NodeState{
			CPUUsage:     n1.CPURawUsage,
			CPUCapacity:  n1.CPUCapacity,
			QueueLength:  nodeQueueSignal(&n1),
			ResponseTime: n1.ResponseTime,
			Requests:     r1,
		},
		Node2: mopso.NodeState{
			CPUUsage:     n2.CPURawUsage,
			CPUCapacity:  n2.CPUCapacity,
			QueueLength:  nodeQueueSignal(&n2),
			ResponseTime: n2.ResponseTime,
			Requests:     r2,
		},
	}
}

// nodeNameByHost memetakan host URL backend ke nama node terdaftar (untuk tracing log).
func (p *NodePool) nodeNameByHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "unknown-node"
	}
	for _, n := range p.nodes {
		if n == nil || n.URL == nil {
			continue
		}
		if strings.EqualFold(n.URL.Host, host) {
			return n.Name
		}
	}
	return "unknown-node"
}

// newReverseProxy membangun reverse proxy dengan hook routing, tracing response, dan error handling.
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
			backendNode := pool.selectBackend()
			if backendNode == nil {
				log.Println("Gagal memilih backend, tidak ada node tersedia.")
				return
			}

			backendNode.RequestCount.Add(1)
			originalHost := req.Host
			req.URL.Scheme = backendNode.URL.Scheme
			req.URL.Host = backendNode.URL.Host
			req.Header.Set(selectedNodeTraceHeader, backendNode.Name)
			req.Header.Set("X-Forwarded-Host", originalHost)
			req.Host = originalHost
		},
		ModifyResponse: func(res *http.Response) error {
			if res == nil || res.Request == nil || res.Request.URL == nil {
				return nil
			}
			selectedNode := strings.TrimSpace(res.Request.Header.Get(selectedNodeTraceHeader))
			if selectedNode == "" {
				selectedNode = pool.nodeNameByHost(res.Request.URL.Host)
			}
			pool.recordPerHitBySelectedNode(selectedNode)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			selectedNode := strings.TrimSpace(r.Header.Get(selectedNodeTraceHeader))
			if selectedNode == "" && r.URL != nil {
				selectedNode = pool.nodeNameByHost(r.URL.Host)
			}
			targetHost := "unknown-host"
			path := ""
			if r.URL != nil {
				if strings.TrimSpace(r.URL.Host) != "" {
					targetHost = r.URL.Host
				}
				path = r.URL.Path
				if r.URL.RawQuery != "" {
					path += "?" + r.URL.RawQuery
				}
			}
			log.Printf(
				"[PROXY-ERROR] Node: %s | TargetHost: %s | Method: %s | Path: %s | Error: %v",
				selectedNode,
				targetHost,
				r.Method,
				path,
				err,
			)
			http.Error(w, "Service tidak tersedia", http.StatusServiceUnavailable)
		},
	}
	return proxy
}

var (
	// Observability inti runtime LB.
	cpuGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "pso_node_cpu_usage", Help: "Penggunaan CPU node backend ternormalisasi kapasitas (%)"},
		[]string{"node_name"},
	)
	cpuRawGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "pso_node_cpu_usage_raw", Help: "Penggunaan CPU node backend absolut sebelum normalisasi (%)"},
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

// init mendaftarkan metric collectors ke registry prometheus global.
func init() {
	prometheus.MustRegister(cpuGauge)
	prometheus.MustRegister(cpuRawGauge)
	prometheus.MustRegister(latencyGauge)
	prometheus.MustRegister(mopsoCostPerRequestGauge)
	prometheus.MustRegister(mopsoActiveModeGauge)
	prometheus.MustRegister(mopsoFitnessScoreGauge)
	prometheus.MustRegister(mopsoParetoArchiveGauge)
}

// envLower membaca env dan menormalkan ke lowercase.
func envLower(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return strings.ToLower(v)
}

// envDurationMS membaca interval dari format durasi Go (`250ms`, `1s`) atau angka ms polos.
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

// normalizeAlgorithm memvalidasi algoritma runtime yang diizinkan.
func normalizeAlgorithm(value string) string {
	switch value {
	case "roundrobin", "fuzzy", "fmopso":
		return value
	default:
		log.Printf("[WARNING] LB_ALGO tidak valid (%s), fallback ke fuzzy", value)
		return "fuzzy"
	}
}

// normalizeTrafficLogMode memvalidasi mode logging dataset fuzzy.
func normalizeTrafficLogMode(value string) string {
	switch value {
	case trafficLogModeWindow, trafficLogModePerHit:
		return value
	default:
		log.Printf("[WARNING] TRAFFIC_LOG_MODE tidak dikenal (%s), fallback ke %s", value, trafficLogModeWindow)
		return trafficLogModeWindow
	}
}

// normalizeMOPSOMode memvalidasi mode bisnis compromise FMOPSO.
func normalizeMOPSOMode(value string) string {
	switch value {
	case "balanced", "performance":
		return value
	default:
		log.Printf("[WARNING] MOPSO_BUSINESS_MODE tidak valid (%s), fallback ke balanced", value)
		return "balanced"
	}
}

// loadRuntimeConfig memuat seluruh konfigurasi runtime dari env.
func loadRuntimeConfig() RuntimeConfig {
	algorithm := normalizeAlgorithm(envLower("LB_ALGO", "fuzzy"))
	mopsoMode := normalizeMOPSOMode(envLower("MOPSO_BUSINESS_MODE", "balanced"))
	trafficLogMode := normalizeTrafficLogMode(envLower("TRAFFIC_LOG_MODE", trafficLogModeWindow))
	return RuntimeConfig{
		Algorithm:         algorithm,
		MOPSOMode:         mopsoMode,
		ParamSource:       envLower("FUZZY_PARAM_SOURCE", "base"),
		TrafficLogMode:    trafficLogMode,
		MetricsInterval:   envDurationMS("METRICS_INTERVAL", 200*time.Millisecond),
		OptimizerInterval: envDurationMS("OPTIMIZER_INTERVAL", 1*time.Second),
		BackendDNS:        []string{"http://api-node1:8080", "http://api-node2:8080"},
	}
}

// initializeFuzzyEngines menginisialisasi:
// 1) engine fuzzy statis dari base/optimized source,
// 2) engine adaptif FMOPSO dari state terakhir yang tersimpan.
func initializeFuzzyEngines(paramSource string) FuzzyBootstrap {
	ensureBaseParamsFile(baseFuzzyParamsPath, DefaultBaseFuzzyParams)
	baseParams := loadFloatArrayWithFallback(baseFuzzyParamsPath, DefaultBaseFuzzyParams, "base fuzzy params")
	baseParams = sanitizeFuzzyParams(baseParams)

	bootstrap := FuzzyBootstrap{
		StaticParams:     append([]float64(nil), baseParams...),
		ParamProfile:     "BASE",
		ActiveStaticFile: baseFuzzyParamsPath,
	}

	switch paramSource {
	case "optimized":
		bootstrap.StaticParams = loadFloatArrayWithFallback(optimizedFuzzyPath, baseParams, "parameter fuzzy teroptimasi")
		bootstrap.ParamProfile = "OPTIMIZED"
		bootstrap.ActiveStaticFile = optimizedFuzzyPath
	default:
		if paramSource != "base" {
			log.Printf("[WARNING] FUZZY_PARAM_SOURCE tidak dikenal (%s), fallback ke base", paramSource)
		}
	}
	bootstrap.StaticParams = sanitizeFuzzyParams(bootstrap.StaticParams)

	bootstrap.FMOPSOParams = loadFloatArrayWithFallback(fmopsoParamsPath, baseParams, "parameter adaptif F-MOPSO")
	bootstrap.FMOPSOParams = sanitizeFuzzyParams(bootstrap.FMOPSOParams)

	StaticFuzzyEngine = fuzzy.NewEngine(bootstrap.StaticParams)
	AdaptiveFMOPSOEngine = fuzzy.NewEngine(bootstrap.FMOPSOParams)

	log.Printf(
		"[ENTRYPOINT][PARAM-SOURCE] PROFILE=%s FUZZY_PARAM_SOURCE=%s ACTIVE_FILE=%s",
		bootstrap.ParamProfile,
		paramSource,
		bootstrap.ActiveStaticFile,
	)
	return bootstrap
}

// newNodePool membangun node pool dengan konfigurasi runtime terpilih.
func newNodePool(client *http.Client, cfg RuntimeConfig, paramProfile string) *NodePool {
	return &NodePool{
		client:         client,
		algorithm:      cfg.Algorithm,
		mopsoMode:      cfg.MOPSOMode,
		paramProfile:   paramProfile,
		trafficLogMode: cfg.TrafficLogMode,
	}
}

// registerBackendNodes mendaftarkan backend statis ke pool.
func registerBackendNodes(pool *NodePool, backendDNS []string) error {
	for i, dns := range backendDNS {
		backendURL, err := url.Parse(dns)
		if err != nil {
			return fmt.Errorf("gagal mem-parse URL backend %q: %w", dns, err)
		}
		nodeName := fmt.Sprintf("api-node%d", i+1)
		pool.nodes = append(pool.nodes, &Node{Name: nodeName, URL: backendURL, CPUCapacity: 100})
	}
	return nil
}

// startRuntimeWorkers menyalakan worker periodik sesuai mode algoritma aktif.
func startRuntimeWorkers(pool *NodePool, cfg RuntimeConfig) {
	pool.startMetricsCollector(cfg.MetricsInterval)
	switch pool.algorithm {
	case "fmopso":
		pool.startMOPSOOptimizer(cfg.OptimizerInterval)
	case "fuzzy":
		if pool.trafficLogMode == trafficLogModePerHit {
			/* Recorder per-hit aktif melalui hook response proxy. */
		} else {
			pool.startFuzzyDatasetRecorder(cfg.OptimizerInterval)
		}
	}
}

// newHTTPMux merakit endpoint prometheus dan reverse proxy utama.
func newHTTPMux(pool *NodePool) *http.ServeMux {
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
	return mux
}

// newHTTPServer membuat konfigurasi server HTTP aplikasi LB.
func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:         ":8080",
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
}

// main merakit seluruh komponen runtime:
// config -> bootstrap engine -> backend pool -> workers -> http server.
func main() {
	ensureDirs("configs", "storage")

	cfg := loadRuntimeConfig()
	fuzzyBootstrap := initializeFuzzyEngines(cfg.ParamSource)
	metricsClient := &http.Client{Timeout: 500 * time.Millisecond}
	pool := newNodePool(metricsClient, cfg, fuzzyBootstrap.ParamProfile)
	if err := registerBackendNodes(pool, cfg.BackendDNS); err != nil {
		log.Fatalf("Gagal mendaftarkan backend: %v", err)
	}

	startRuntimeWorkers(pool, cfg)
	mux := newHTTPMux(pool)
	server := newHTTPServer(mux)

	log.Printf(
		"Memulai Load Balancer di port :8080 (ALGO=%s, PARAM_PROFILE=%s, MOPSO_MODE=%s, TRAFFIC_LOG_MODE=%s, METRICS_INTERVAL=%s, OPT_INTERVAL=%s)...",
		pool.algorithm,
		pool.activeParamProfile(),
		pool.mopsoMode,
		pool.trafficLogMode,
		cfg.MetricsInterval,
		cfg.OptimizerInterval,
	)

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Gagal memulai server: %v", err)
	}
}
