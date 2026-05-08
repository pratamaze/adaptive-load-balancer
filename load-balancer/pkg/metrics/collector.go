package metrics

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"load-balancer/pkg/proxy"
	lbtypes "load-balancer/pkg/types"

	"github.com/prometheus/client_golang/prometheus"
)

type NodeMetrics struct {
	NodeName           string  `json:"node_name"`
	CPUUsage           float64 `json:"cpu_usage"`
	CPUUsageRaw        float64 `json:"cpu_usage_raw"`
	CPUUsageNormalized float64 `json:"cpu_usage_normalized"`
	MemoryUsage        float64 `json:"memory_usage"`
	MemoryUsageNorm    float64 `json:"memory_usage_normalized"`
	LoadAverage1       float64 `json:"load_average_1"`
	RequestLatencyMS   float64 `json:"request_latency_ms"`
	InflightRequests   float64 `json:"inflight_requests"`
	CPUCapacity        float64 `json:"cpu_capacity_percent"`
}

var (
	cpuGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "pso_node_cpu_usage", Help: "Penggunaan CPU node backend ternormalisasi kapasitas (%)"},
		[]string{"node_name"},
	)
	cpuRawGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "pso_node_cpu_usage_raw", Help: "Mirror penggunaan CPU node backend (nilai normalized untuk kompatibilitas historis)"},
		[]string{"node_name"},
	)
	latencyGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "pso_node_latency_ms", Help: "Latensi komunikasi ke node (ms)"},
		[]string{"node_name"},
	)

	registerMetricsOnce sync.Once
)

func init() {
	registerMetricsOnce.Do(func() {
		prometheus.MustRegister(cpuGauge)
		prometheus.MustRegister(cpuRawGauge)
		prometheus.MustRegister(latencyGauge)
	})
}

func StartCollector(nodes []*lbtypes.BackendNode, client *http.Client, interval time.Duration, paramProfile string) {
	if client == nil || len(nodes) == 0 {
		return
	}
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}

	updateAllMetrics(nodes, client, paramProfile)
	ticker := time.NewTicker(interval)
	for range ticker.C {
		updateAllMetrics(nodes, client, paramProfile)
	}
}

func updateAllMetrics(nodes []*lbtypes.BackendNode, client *http.Client, paramProfile string) {
	var wg sync.WaitGroup
	for _, node := range nodes {
		if node == nil {
			continue
		}
		wg.Add(1)
		go func(n *lbtypes.BackendNode) {
			defer wg.Done()
			getRealNodeMetrics(n, client, paramProfile)
		}(node)
	}
	wg.Wait()
}

func getRealNodeMetrics(node *lbtypes.BackendNode, client *http.Client, paramProfile string) {
	if node == nil || node.URL == nil {
		return
	}
	metricsURL := node.URL.String() + "/metrics"
	startTime := time.Now()

	resp, err := client.Get(metricsURL)
	responseTime := time.Since(startTime).Seconds() * 1000
	if err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal mengambil metrik dari %s: %v\n", activeParamProfile(paramProfile), node.Name, err)
		proxy.ApplyMetricPenalty(node)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal membaca body dari %s: %v\n", activeParamProfile(paramProfile), node.Name, err)
		proxy.ApplyMetricPenalty(node)
		return
	}

	var parsed NodeMetrics
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal parse JSON dari %s: %v\n", activeParamProfile(paramProfile), node.Name, err)
		proxy.ApplyMetricPenalty(node)
		return
	}

	normalizedCPU := parsed.CPUUsageNormalized
	if normalizedCPU < 0 {
		normalizedCPU = 0
	}
	if normalizedCPU > 100 {
		normalizedCPU = 100
	}

	normalizedMem := parsed.MemoryUsageNorm
	if normalizedMem <= 0 {
		normalizedMem = parsed.MemoryUsage
	}
	if normalizedMem < 0 {
		normalizedMem = 0
	}
	if normalizedMem > 100 {
		normalizedMem = 100
	}

	responseMS := responseTime
	if parsed.RequestLatencyMS > 0 {
		responseMS = parsed.RequestLatencyMS
	}

	node.UpdateMetrics(
		normalizedCPU,
		normalizedCPU,
		parsed.LoadAverage1,
		parsed.InflightRequests,
		normalizedMem,
		responseMS,
		100,
	)

	cpuGauge.WithLabelValues(node.Name).Set(normalizedCPU)
	cpuRawGauge.WithLabelValues(node.Name).Set(normalizedCPU)
	latencyGauge.WithLabelValues(node.Name).Set(responseMS)
}

func activeParamProfile(profile string) string {
	p := strings.TrimSpace(profile)
	if p == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(p)
}
