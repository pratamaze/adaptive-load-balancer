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

func (p *NodePool) writeFuzzyTrainingSample(windowMS int64, mode string, r1, r2 int64) {
	if len(p.nodes) < 2 {
		return
	}
	totalReq := r1 + r2
	if totalReq <= 0 {
		return
	}

	p.nodes[0].mutex.RLock()
	n1 := *p.nodes[0]
	p.nodes[0].mutex.RUnlock()
	p.nodes[1].mutex.RLock()
	n2 := *p.nodes[1]
	p.nodes[1].mutex.RUnlock()

	m1 := fuzzy.NodeMetrics{CPU: n1.CPUUsage, QueueLength: nodeQueueSignal(&n1), RespTime: n1.ResponseTime}
	m2 := fuzzy.NodeMetrics{CPU: n2.CPUUsage, QueueLength: nodeQueueSignal(&n2), RespTime: n2.ResponseTime}
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
		formatFloatCSV(m1.QueueLength),
		formatFloatCSV(m2.QueueLength),
		formatFloatCSV(n1.ResponseTime),
		formatFloatCSV(n2.ResponseTime),
		formatFloatCSV(score1),
		formatFloatCSV(score2),
		formatFloatCSV(osIdleCPU10),
	}

	emitDatasetCSVRecord(record)
}

func (p *NodePool) recordPerHitBySelectedNode(selectedNode string) {
	if p.algorithm != "fuzzy" {
		return
	}
	if p.trafficLogMode != trafficLogModePerHit {
		return
	}
	if len(p.nodes) < 2 {
		return
	}

	emitDatasetCSVHeader()

	r1, r2 := int64(0), int64(0)
	switch strings.TrimSpace(selectedNode) {
	case p.nodes[0].Name:
		r1 = 1
	case p.nodes[1].Name:
		r2 = 1
	default:
		return
	}
	p.writeFuzzyTrainingSample(0, trafficLogModePerHit, r1, r2)
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
