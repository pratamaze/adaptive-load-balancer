package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"load-balancer/pkg/fuzzy"
)

const (
	trafficLogModeWindow = "window"
	trafficLogModePerHit = "per_hit"
	cpuUsageUnitRaw      = "raw_percent_of_host"
	datasetCSVPrefix     = "[DATASET_CSV]"
)

var fuzzyTrainingDataHeader = []string{
	"timestamp_utc",
	"window_ms",
	"traffic_log_mode",
	"cpu_usage_unit",
	"node1_name",
	"node2_name",
	"node1_requests",
	"node2_requests",
	"total_requests",
	"node1_cpu_raw_usage",
	"node2_cpu_raw_usage",
	"node1_cpu_normalized_usage",
	"node2_cpu_normalized_usage",
	"node1_cpu_capacity",
	"node2_cpu_capacity",
	"node1_inflight",
	"node2_inflight",
	"node1_backend_inflight",
	"node2_backend_inflight",
	"node1_queue",
	"node2_queue",
	"node1_response_ms",
	"node2_response_ms",
	"node1_fuzzy_score",
	"node2_fuzzy_score",
	"os_idle_cpu",
}

var (
	datasetCSVHeaderLine = strings.Join(fuzzyTrainingDataHeader, ",")
	datasetCSVHeaderOnce sync.Once
	datasetCSVLogMu      sync.Mutex
)

func formatFloatCSV(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}

func emitDatasetCSVLine(csvLine string) {
	datasetCSVLogMu.Lock()
	defer datasetCSVLogMu.Unlock()
	fmt.Printf("%s %s\n", datasetCSVPrefix, csvLine)
}

func emitDatasetCSVHeader() {
	datasetCSVHeaderOnce.Do(func() {
		emitDatasetCSVLine(datasetCSVHeaderLine)
	})
}

func emitDatasetCSVRecord(record []string) {
	emitDatasetCSVHeader()
	emitDatasetCSVLine(strings.Join(record, ","))
}

type datasetNodeSample struct {
	Name            string
	CPURawUsage     float64
	CPUUsage        float64
	CPUCapacity     float64
	BackendInflight float64
	ProxyInflight   float64
	ResponseMS      float64
}

func sampleNodeForDataset(node *Node) datasetNodeSample {
	node.mutex.RLock()
	out := datasetNodeSample{
		Name:            node.Name,
		CPURawUsage:     node.CPURawUsage,
		CPUUsage:        node.CPUUsage,
		CPUCapacity:     node.CPUCapacity,
		BackendInflight: node.InflightReq,
		ResponseMS:      node.ResponseTime,
	}
	node.mutex.RUnlock()
	out.ProxyInflight = node.proxyInflightValue()
	if lastProxyRespMS := node.lastProxyLatencyMS(); lastProxyRespMS > 0 {
		out.ResponseMS = lastProxyRespMS
	}
	return out
}

func (p *NodePool) writeFuzzyTrainingSample(windowMS int64, mode string, r1, r2 int64) {
	if StaticFuzzyEngine == nil {
		log.Printf("[DATASET] StaticFuzzyEngine nil, skip sample mode=%s", mode)
		return
	}
	if len(p.nodes) < 2 {
		return
	}
	totalReq := r1 + r2
	if totalReq <= 0 {
		return
	}

	n1 := sampleNodeForDataset(p.nodes[0])
	n2 := sampleNodeForDataset(p.nodes[1])
	// Queue untuk dataset dipetakan ke inflight riil agar sample merefleksikan antrean aktif.
	queue1 := n1.ProxyInflight
	queue2 := n2.ProxyInflight

	m1 := fuzzy.NodeMetrics{CPU: n1.CPUUsage, QueueLength: queue1, RespTime: n1.ResponseMS}
	m2 := fuzzy.NodeMetrics{CPU: n2.CPUUsage, QueueLength: queue2, RespTime: n2.ResponseMS}
	score1 := StaticFuzzyEngine.CalculateMamdani(m1, myRules)
	score2 := StaticFuzzyEngine.CalculateMamdani(m2, myRules)

	record := []string{
		time.Now().UTC().Format(time.RFC3339Nano),
		strconv.FormatInt(windowMS, 10),
		mode,
		cpuUsageUnitRaw,
		n1.Name,
		n2.Name,
		strconv.FormatInt(r1, 10),
		strconv.FormatInt(r2, 10),
		strconv.FormatInt(totalReq, 10),
		formatFloatCSV(n1.CPURawUsage),
		formatFloatCSV(n2.CPURawUsage),
		formatFloatCSV(n1.CPUUsage),
		formatFloatCSV(n2.CPUUsage),
		formatFloatCSV(n1.CPUCapacity),
		formatFloatCSV(n2.CPUCapacity),
		formatFloatCSV(n1.ProxyInflight),
		formatFloatCSV(n2.ProxyInflight),
		formatFloatCSV(n1.BackendInflight),
		formatFloatCSV(n2.BackendInflight),
		formatFloatCSV(m1.QueueLength),
		formatFloatCSV(m2.QueueLength),
		formatFloatCSV(n1.ResponseMS),
		formatFloatCSV(n2.ResponseMS),
		formatFloatCSV(score1),
		formatFloatCSV(score2),
		formatFloatCSV(osIdleCPU10),
	}

	emitDatasetCSVRecord(record)
}

func (p *NodePool) recordPerHitByDecisionSnapshot(snapshot *DecisionSnapshot, selectedNodeFallback string) {
	if p.algorithm != "fuzzy" {
		return
	}
	if p.trafficLogMode != trafficLogModePerHit {
		return
	}
	if len(p.nodes) < 2 {
		return
	}
	if snapshot == nil {
		log.Printf("[DATASET] Snapshot decision tidak tersedia, skip per-hit CSV")
		return
	}

	node1Name := snapshot.Node1Name
	if strings.TrimSpace(node1Name) == "" {
		node1Name = p.nodes[0].Name
	}
	node2Name := snapshot.Node2Name
	if strings.TrimSpace(node2Name) == "" {
		node2Name = p.nodes[1].Name
	}

	r1, r2 := int64(0), int64(0)
	selectedNode := strings.TrimSpace(snapshot.SelectedNode)
	if selectedNode == "" {
		selectedNode = strings.TrimSpace(selectedNodeFallback)
	}
	switch selectedNode {
	case node1Name:
		r1 = 1
	case node2Name:
		r2 = 1
	default:
		return
	}

	cpuCap1 := snapshot.Node1CPUCap
	cpuCap2 := snapshot.Node2CPUCap
	if cpuCap1 <= 0 {
		cpuCap1 = 100
	}
	if cpuCap2 <= 0 {
		cpuCap2 = 100
	}

	record := []string{
		time.Now().UTC().Format(time.RFC3339Nano),
		"0",
		trafficLogModePerHit,
		cpuUsageUnitRaw,
		node1Name,
		node2Name,
		strconv.FormatInt(r1, 10),
		strconv.FormatInt(r2, 10),
		strconv.FormatInt(r1+r2, 10),
		formatFloatCSV(snapshot.Node1CPURaw),
		formatFloatCSV(snapshot.Node2CPURaw),
		formatFloatCSV(snapshot.CPU1),
		formatFloatCSV(snapshot.CPU2),
		formatFloatCSV(cpuCap1),
		formatFloatCSV(cpuCap2),
		formatFloatCSV(snapshot.Q1),
		formatFloatCSV(snapshot.Q2),
		formatFloatCSV(snapshot.Node1BackendQ),
		formatFloatCSV(snapshot.Node2BackendQ),
		formatFloatCSV(snapshot.Q1),
		formatFloatCSV(snapshot.Q2),
		formatFloatCSV(snapshot.RT1),
		formatFloatCSV(snapshot.RT2),
		formatFloatCSV(snapshot.Score1),
		formatFloatCSV(snapshot.Score2),
		formatFloatCSV(osIdleCPU10),
	}

	emitDatasetCSVRecord(record)
}

func (p *NodePool) startFuzzyDatasetRecorder(interval time.Duration) {
	if p.algorithm != "fuzzy" {
		log.Printf("[DATASET] Recorder fuzzy dilewati karena ALGO=%s", p.algorithm)
		return
	}
	emitDatasetCSVHeader()
	log.Printf("[DATASET] Recorder fuzzy aktif via stdout tag %s setiap %s", datasetCSVPrefix, interval)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

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
			p.writeFuzzyTrainingSample(interval.Milliseconds(), trafficLogModeWindow, r1, r2)
		}
	}()
}
