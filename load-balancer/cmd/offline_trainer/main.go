package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"load-balancer/pkg/mopso"
)

const (
	baseFuzzyParamsPath      = "configs/base_fuzzy_params.json"
	optimizedFuzzyParamsPath = "configs/optimized_fuzzy_params.json"
	trafficDatasetPath       = "logs/traffic_dataset.csv"
	mainDecisionLogPath      = "logs/hasil_fpso.log"
	liveDecisionLogPath      = "../logs/hasil_pso_live.log"
)

type TrafficData struct {
	Timestamp     string
	TotalRequests float64
	AvgCostPerReq float64
}

type KneePoint struct {
	Params    []float64       `json:"params"`
	Objective mopso.Objective `json:"objective"`
}

func main() {
	baseParams, err := readFloatArray(baseFuzzyParamsPath)
	if err != nil {
		log.Fatalf("gagal membaca base params: %v", err)
	}

	dataset, err := readTrafficDataset(trafficDatasetPath)
	if err != nil {
		log.Fatalf("gagal membaca dataset traffic: %v", err)
	}
	if shouldBackfillFromDecisionLogs(dataset) {
		backfilled, err := readDecisionLogDataset(mainDecisionLogPath, liveDecisionLogPath)
		if err != nil {
			log.Fatalf("gagal membaca decision logs: %v", err)
		}
		if len(backfilled) > 0 {
			dataset = append(dataset, backfilled...)
			log.Printf("decision-log backfill aktif: +%d baris dari log historis", len(backfilled))
		}
	}
	if len(dataset) == 0 {
		log.Fatalf("dataset kosong: %s", trafficDatasetPath)
	}

	knee, err := trainKneePoint(baseParams, dataset)
	if err != nil {
		log.Fatalf("offline training gagal: %v", err)
	}

	if err := writeJSON(optimizedFuzzyParamsPath, knee.Params); err != nil {
		log.Fatalf("gagal menulis optimized params: %v", err)
	}

	log.Printf("offline trainer selesai")
	log.Printf("dataset rows=%d", len(dataset))
	log.Printf("knee objective -> f1=%.8f f2=%.8f", knee.Objective.Imbalance, knee.Objective.PeakLoad)
	log.Printf("saved: %s", optimizedFuzzyParamsPath)
}

func shouldBackfillFromDecisionLogs(dataset []TrafficData) bool {
	if len(dataset) == 0 {
		return true
	}
	for _, row := range dataset {
		if row.TotalRequests > 1 {
			return true
		}
	}
	return false
}

func trainKneePoint(baseParams []float64, dataset []TrafficData) (KneePoint, error) {
	// Template/mock pemanggilan MOPSO: dataset di-aggregate menjadi historical snapshot,
	// lalu optimizer existing dipanggil tanpa mengubah fuzzy engine.
	snapshot := buildSnapshotFromDataset(dataset)
	result := mopso.OptimizeReplay(baseParams, snapshot)
	if len(result.Archive) == 0 || len(result.Compromises) == 0 {
		return KneePoint{}, fmt.Errorf("pareto archive kosong")
	}

	active, ok := mopso.ActiveByMode(result, "balanced")
	if !ok {
		return KneePoint{}, fmt.Errorf("gagal memilih compromise solution")
	}

	return KneePoint{
		Params:    active.Solution.Params,
		Objective: active.Solution.Objective,
	}, nil
}

func buildSnapshotFromDataset(dataset []TrafficData) mopso.HistoricalSnapshot {
	var totalReq float64
	var weightedCost float64
	for _, row := range dataset {
		if row.TotalRequests <= 0 {
			continue
		}
		totalReq += row.TotalRequests
		weightedCost += row.TotalRequests * row.AvgCostPerReq
	}
	if totalReq <= 0 {
		totalReq = 1
	}
	avgCost := weightedCost / totalReq

	req1 := int64(math.Round(totalReq * 0.35))
	req2 := int64(math.Round(totalReq * 0.65))
	if req1 < 1 {
		req1 = 1
	}
	if req2 < 1 {
		req2 = 1
	}

	// Nilai ini adalah estimasi replay offline (template) berbasis dataset historis.
	osIdle := 10.0
	cpu1 := osIdle + avgCost*float64(req1)
	cpu2 := osIdle + avgCost*float64(req2)
	if cpu1 > 100 {
		cpu1 = 100
	}
	if cpu2 > 100 {
		cpu2 = 100
	}

	queueBase := avgCost * 120
	if queueBase > 1000 {
		queueBase = 1000
	}
	respBase := avgCost * 1000
	if respBase > 1000 {
		respBase = 1000
	}

	return mopso.HistoricalSnapshot{
		OSIdleCPU10: osIdle,
		Node1: mopso.NodeState{
			CPUUsage:     cpu1,
			CPUCapacity:  100,
			QueueLength:  queueBase * 0.8,
			ResponseTime: respBase * 0.9,
			Requests:     req1,
		},
		Node2: mopso.NodeState{
			CPUUsage:     cpu2,
			CPUCapacity:  200,
			QueueLength:  queueBase * 1.1,
			ResponseTime: respBase * 1.1,
			Requests:     req2,
		},
	}
}

func readTrafficDataset(path string) ([]TrafficData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true

	first, err := r.Read()
	if err != nil {
		return nil, err
	}
	rows := make([]TrafficData, 0, 2048)
	idxTS, idxReq, idxCost, headerOK := mapHeader(first)
	if !headerOK {
		// Backward-compatibility: file lama tanpa header.
		// Asumsikan urutan kolom: Timestamp, TotalRequests, AvgCostPerReq.
		row, ok := parseTrafficRow(first, 0, 1, 2)
		if ok {
			rows = append(rows, row)
		}
		idxTS, idxReq, idxCost = 0, 1, 2
	}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) <= maxInt(idxTS, maxInt(idxReq, idxCost)) {
			continue
		}
		row, ok := parseTrafficRow(rec, idxTS, idxReq, idxCost)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}

	return rows, nil
}

func mapHeader(header []string) (int, int, int, bool) {
	idxTS, idxReq, idxCost := -1, -1, -1
	for i, h := range header {
		n := normalizeHeader(h)
		switch n {
		case "timestamp":
			idxTS = i
		case "totalrequests", "total_requests", "totalrequestsin5s", "total_requests_in_5s":
			idxReq = i
		case "avgcostperreq", "avg_cost_per_req", "avgcostperreqin5s", "avg_cost_per_req_in_5s":
			idxCost = i
		}
	}
	if idxTS < 0 || idxReq < 0 || idxCost < 0 {
		return 0, 0, 0, false
	}
	return idxTS, idxReq, idxCost, true
}

func normalizeHeader(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "\ufeff")
	v = strings.ToLower(v)
	v = strings.ReplaceAll(v, " ", "")
	v = strings.ReplaceAll(v, "-", "_")
	return v
}

func parseTrafficRow(rec []string, idxTS, idxReq, idxCost int) (TrafficData, bool) {
	if len(rec) <= maxInt(idxTS, maxInt(idxReq, idxCost)) {
		return TrafficData{}, false
	}
	req, err1 := strconv.ParseFloat(strings.TrimSpace(rec[idxReq]), 64)
	cost, err2 := strconv.ParseFloat(strings.TrimSpace(rec[idxCost]), 64)
	if err1 != nil || err2 != nil {
		return TrafficData{}, false
	}
	if req < 0 {
		req = 0
	}
	if cost < 0 {
		cost = 0
	}
	return TrafficData{
		Timestamp:     strings.TrimSpace(rec[idxTS]),
		TotalRequests: req,
		AvgCostPerReq: cost,
	}, true
}

var (
	reDecisionTimestamp = regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`)
	reNodeLatency       = regexp.MustCompile(`\[([a-zA-Z0-9_-]+):[^\]]*?Lat=([0-9]+(?:\.[0-9]+)?)ms`)
	reSelectedNode      = regexp.MustCompile(`TERPILIH(?:\s*\([^)]+\))?:\s*([a-zA-Z0-9_-]+)`)
)

func readDecisionLogDataset(paths ...string) ([]TrafficData, error) {
	rows := make([]TrafficData, 0, 4096)
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		fileRows, err := parseDecisionLogFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		rows = append(rows, fileRows...)
	}
	return rows, nil
}

func parseDecisionLogFile(path string) ([]TrafficData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rows := make([]TrafficData, 0, 4096)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		row, ok := parseDecisionLogLine(scanner.Text())
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func parseDecisionLogLine(line string) (TrafficData, bool) {
	if !strings.Contains(line, "[DECISION]") {
		return TrafficData{}, false
	}

	tsMatch := reDecisionTimestamp.FindString(line)
	if tsMatch == "" {
		return TrafficData{}, false
	}
	parsedTS, err := time.ParseInLocation("2006/01/02 15:04:05", tsMatch, time.Local)
	if err != nil {
		return TrafficData{}, false
	}

	latByNode := map[string]float64{}
	nodeMatches := reNodeLatency.FindAllStringSubmatch(line, -1)
	for _, m := range nodeMatches {
		if len(m) < 3 {
			continue
		}
		lat, err := strconv.ParseFloat(strings.TrimSpace(m[2]), 64)
		if err != nil {
			continue
		}
		if lat < 0 {
			lat = 0
		}
		latByNode[m[1]] = lat
	}
	if len(latByNode) == 0 {
		return TrafficData{}, false
	}

	selected := ""
	if m := reSelectedNode.FindStringSubmatch(line); len(m) >= 2 {
		selected = m[1]
	}

	cost := 0.0
	if selected != "" {
		if v, ok := latByNode[selected]; ok {
			cost = v
		}
	}
	if cost <= 0 {
		var sum float64
		var count int
		for _, v := range latByNode {
			if v <= 0 {
				continue
			}
			sum += v
			count++
		}
		if count == 0 {
			return TrafficData{}, false
		}
		cost = sum / float64(count)
	}

	return TrafficData{
		Timestamp:     parsedTS.UTC().Format(time.RFC3339),
		TotalRequests: 1,
		AvgCostPerReq: cost,
	}, true
}

func readFloatArray(path string) ([]float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []float64
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeJSON(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
