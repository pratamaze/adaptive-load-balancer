package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"load-balancer/cmd/mopso-train/internal/mopso"
)

var defaultBaseParams = []float64{
	0, 40, 75, 60, 80, 95, 85, 95, 100,
	0, 50, 150, 100, 250, 400, 300, 500, 1000,
	0, 150, 300, 200, 500, 800, 600, 850, 1000,
}

func main() {
	datasetPath := flag.String("dataset", "storage/fuzzy_training_data.csv", "path CSV dataset training dari fuzzy runtime")
	basePath := flag.String("base", "configs/base_fuzzy_params.json", "path JSON base parameter fuzzy")
	outParams := flag.String("out-params", "storage/optimized_fuzzy_params.json", "output JSON parameter terbaik")
	outReport := flag.String("out-report", "storage/mopso_offline_report.json", "output JSON ringkasan hasil training")
	particles := flag.Int("particles", 30, "jumlah partikel MOPSO")
	iterations := flag.Int("iterations", 3000, "jumlah iterasi MOPSO")
	spread := flag.Float64("spread", 8.0, "jitter awal parameter dari base")
	seed := flag.Int64("seed", 0, "seed random (0 = otomatis)")
	runs := flag.Int("runs", 5, "jumlah run independen MOPSO, dipilih run terbaik")
	allowRegression := flag.Bool("allow-regression", false, "izinkan hasil optimasi yang tidak mengalahkan base objective")
	flag.Parse()

	samples, err := loadOfflineSamples(*datasetPath)
	if err != nil {
		fatalf("gagal baca dataset: %v", err)
	}

	baseParams, err := loadBaseParams(*basePath)
	if err != nil {
		fatalf("gagal baca base params: %v", err)
	}

	totalRuns := *runs
	if totalRuns <= 0 {
		totalRuns = 1
	}

	baseSeed := *seed
	if baseSeed == 0 {
		baseSeed = time.Now().UnixNano()
	}

	type runSummary struct {
		RunIndex int                  `json:"run_index"`
		Seed     int64                `json:"seed"`
		Score    float64              `json:"score_balanced"`
		Result   *mopso.OfflineResult `json:"result"`
		Err      string               `json:"error,omitempty"`
	}

	summaries := make([]runSummary, 0, totalRuns)
	var bestResult *mopso.OfflineResult
	bestScore := math.Inf(1)
	var bestRun runSummary

	for i := 0; i < totalRuns; i++ {
		runSeed := baseSeed + int64(i)
		cfg := mopso.OfflineConfig{
			Particles:     *particles,
			Iterations:    *iterations,
			InitialSpread: *spread,
			Seed:          runSeed,
		}
		result, err := mopso.OptimizeOffline(samples, baseParams, cfg)
		if err != nil {
			summaries = append(summaries, runSummary{
				RunIndex: i + 1,
				Seed:     runSeed,
				Score:    math.Inf(1),
				Err:      err.Error(),
			})
			continue
		}
		score := balancedScore(result.BestBalanced.Objective)
		summaries = append(summaries, runSummary{
			RunIndex: i + 1,
			Seed:     runSeed,
			Score:    score,
			Result:   &result,
		})

		if bestResult == nil || score < bestScore {
			cp := result
			bestResult = &cp
			bestScore = score
			bestRun = summaries[len(summaries)-1]
		}
	}

	if bestResult == nil {
		fatalf("training MOPSO gagal di semua run")
	}
	result := *bestResult

	// Pilih kandidat sane terbaik dari semua run yang sukses.
	var saneResult *mopso.OfflineResult
	var saneRun runSummary
	var saneCandidate mopso.OfflineSolution
	saneScore := math.Inf(1)
	for i := range summaries {
		if summaries[i].Result == nil {
			continue
		}
		cand, ok := chooseSaneCandidate(*summaries[i].Result)
		if !ok {
			continue
		}
		score := balancedScore(cand.Objective)
		if score < saneScore {
			cp := *summaries[i].Result
			saneResult = &cp
			saneRun = summaries[i]
			saneCandidate = cand
			saneScore = score
		}
	}

	if saneResult != nil {
		result = *saneResult
		bestRun = saneRun
		result.BestBalanced = saneCandidate
	} else {
		// Fallback aman: repair kandidat terbaik dari run terbaik agar tidak langsung gagal total.
		repaired := forceSaneSolution(result.BestBalanced)
		if !isSaneParams(repaired.Params) {
			fatalf("tidak ada kandidat parameter yang sane di semua run; coba tingkatkan runs/particles/iterations atau perbesar spread")
		}
		result.BestBalanced = repaired
		log.Printf("[WARNING] Semua kandidat pareto mentah tidak sane, memakai hasil repair aman dari best run")
	}
	chosenScore := balancedScore(result.BestBalanced.Objective)

	if len(result.BestBalanced.Params) != mopso.Dimensions {
		fatalf("hasil parameter tidak valid")
	}

	optimizedObj, err := mopso.EvaluateOfflineObjective(result.BestBalanced.Params, samples)
	if err != nil {
		fatalf("gagal evaluasi objective optimized: %v", err)
	}
	baseObj, err := mopso.EvaluateOfflineObjective(baseParams, samples)
	if err != nil {
		fatalf("gagal evaluasi objective base: %v", err)
	}
	result.BestBalanced.Objective = optimizedObj
	chosenScore = balancedScore(result.BestBalanced.Objective)
	baseScore := balancedScore(baseObj)
	if !*allowRegression && chosenScore >= baseScore {
		fatalf("hasil optimasi tidak lebih baik dari base (base=%.6f, optimized=%.6f). tetap gunakan base params", baseScore, chosenScore)
	}

	if err := saveJSON(*outParams, result.BestBalanced.Params); err != nil {
		fatalf("gagal simpan params optimized: %v", err)
	}
	reportPayload := struct {
		SelectedRun runSummary          `json:"selected_run"`
		Runs        []runSummary        `json:"runs"`
		Result      mopso.OfflineResult `json:"result"`
	}{
		SelectedRun: bestRun,
		Runs:        summaries,
		Result:      result,
	}
	if err := saveJSON(*outReport, reportPayload); err != nil {
		fatalf("gagal simpan report training: %v", err)
	}

	fmt.Printf("Training selesai. runs=%d (best run #%d seed=%d)\n", totalRuns, bestRun.RunIndex, bestRun.Seed)
	fmt.Printf("samples=%d used=%d pareto=%d\n", result.SampleCount, result.UsedSamples, len(result.Archive))
	fmt.Printf("Base objective -> DI=%.6f, BCU=%.6f, Score=%.6f\n", baseObj.DI, baseObj.BCU, baseScore)
	fmt.Printf("Best balanced -> DI=%.6f, BCU=%.6f\n", result.BestBalanced.Objective.DI, result.BestBalanced.Objective.BCU)
	fmt.Printf("Best score    -> DI + (1-BCU) = %.6f\n", chosenScore)
	fmt.Printf("Params saved  : %s\n", absPath(*outParams))
	fmt.Printf("Report saved  : %s\n", absPath(*outReport))
}

func balancedScore(obj mopso.OfflineObjective) float64 {
	return obj.DI + (1 - obj.BCU)
}

func forceSaneSolution(sol mopso.OfflineSolution) mopso.OfflineSolution {
	out := mopso.OfflineSolution{
		Params:    append([]float64(nil), sol.Params...),
		Objective: sol.Objective,
	}
	if len(out.Params) != mopso.Dimensions {
		return out
	}
	forceSaneParams(out.Params)
	return out
}

func chooseSaneCandidate(result mopso.OfflineResult) (mopso.OfflineSolution, bool) {
	if len(result.BestBalanced.Params) == mopso.Dimensions && isSaneParams(result.BestBalanced.Params) {
		return result.BestBalanced, true
	}
	for i := range result.Archive {
		if len(result.Archive[i].Params) != mopso.Dimensions {
			continue
		}
		if isSaneParams(result.Archive[i].Params) {
			return result.Archive[i], true
		}
	}
	return mopso.OfflineSolution{}, false
}

func isSaneParams(params []float64) bool {
	// Minimal lebar segitiga agar membership tidak kolaps.
	minCPUWidth := 2.0
	minQueueRespWidth := 20.0

	for i := 0; i+2 < len(params); i += 3 {
		a := params[i]
		b := params[i+1]
		c := params[i+2]
		if !(a <= b && b <= c) {
			return false
		}
		minW := minQueueRespWidth
		if i <= 6 {
			minW = minCPUWidth
		}
		if (b-a) < minW || (c-b) < minW {
			return false
		}
	}

	// Urutan label Low < Medium < High berdasarkan puncak (b) tiap variabel.
	for _, idx := range [][3]int{{1, 4, 7}, {10, 13, 16}, {19, 22, 25}} {
		if !(params[idx[0]] < params[idx[1]] && params[idx[1]] < params[idx[2]]) {
			return false
		}
	}
	return true
}

func forceSaneParams(params []float64) {
	for i := 0; i+2 < len(params); i += 3 {
		lo, hi := boundsByDim(i)
		minGap := 20.0
		if i <= 6 {
			minGap = 2.0
		}
		a := clampLocal(params[i], lo, hi)
		b := clampLocal(params[i+1], lo, hi)
		c := clampLocal(params[i+2], lo, hi)
		if a > b {
			a, b = b, a
		}
		if b > c {
			b, c = c, b
		}
		if a > b {
			a, b = b, a
		}
		if b < a+minGap {
			b = a + minGap
		}
		if c < b+minGap {
			c = b + minGap
		}
		if c > hi {
			c = hi
			if b > c-minGap {
				b = c - minGap
			}
			if a > b-minGap {
				a = b - minGap
			}
		}
		if a < lo {
			a = lo
		}
		params[i] = clampLocal(a, lo, hi)
		params[i+1] = clampLocal(b, lo, hi)
		params[i+2] = clampLocal(c, lo, hi)
	}

	enforceOrderedPeaksLocal(params, 0, 2.0, 100.0)
	enforceOrderedPeaksLocal(params, 9, 20.0, 1000.0)
	enforceOrderedPeaksLocal(params, 18, 20.0, 1000.0)
}

func enforceOrderedPeaksLocal(params []float64, start int, minGap, hi float64) {
	peaks := []float64{params[start+1], params[start+4], params[start+7]}
	sort.Float64s(peaks)
	if peaks[1] < peaks[0]+minGap {
		peaks[1] = peaks[0] + minGap
	}
	if peaks[2] < peaks[1]+minGap {
		peaks[2] = peaks[1] + minGap
	}
	if peaks[2] > hi {
		peaks[2] = hi
		if peaks[1] > peaks[2]-minGap {
			peaks[1] = peaks[2] - minGap
		}
		if peaks[0] > peaks[1]-minGap {
			peaks[0] = peaks[1] - minGap
		}
	}
	if peaks[0] < 0 {
		peaks[0] = 0
	}
	params[start+1] = peaks[0]
	params[start+4] = peaks[1]
	params[start+7] = peaks[2]

	// Koreksi ulang tiap triangle agar puncak tetap berada di dalam segitiga
	// dengan lebar minimum setelah peak diurutkan.
	for tri := 0; tri < 3; tri++ {
		aIdx := start + tri*3
		bIdx := aIdx + 1
		cIdx := aIdx + 2
		lo, hiTri := boundsByDim(aIdx)
		b := clampLocal(params[bIdx], lo, hiTri)
		a := clampLocal(params[aIdx], lo, hiTri)
		c := clampLocal(params[cIdx], lo, hiTri)

		if a > b-minGap {
			a = b - minGap
		}
		if c < b+minGap {
			c = b + minGap
		}
		a = clampLocal(a, lo, hiTri)
		c = clampLocal(c, lo, hiTri)

		if a > b {
			a = clampLocal(b-minGap, lo, hiTri)
		}
		if c < b {
			c = clampLocal(b+minGap, lo, hiTri)
		}
		params[aIdx] = a
		params[bIdx] = b
		params[cIdx] = c
	}
}

func boundsByDim(d int) (float64, float64) {
	if d <= 8 {
		return 0, 100
	}
	return 0, 1000
}

func clampLocal(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func loadOfflineSamples(path string) ([]mopso.OfflineSample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	index := mapHeaderIndex(header)

	req1Idx, err := findHeader(index, "node1_requests")
	if err != nil {
		return nil, err
	}
	req2Idx, err := findHeader(index, "node2_requests")
	if err != nil {
		return nil, err
	}
	cpu1Idx, err := findHeader(index, "node1_cpu_raw_usage", "node1_cpu_usage_raw")
	if err != nil {
		return nil, fmt.Errorf("dataset tidak valid: butuh kolom CPU raw node1 (contoh: node1_cpu_raw_usage)")
	}
	cpu2Idx, err := findHeader(index, "node2_cpu_raw_usage", "node2_cpu_usage_raw")
	if err != nil {
		return nil, fmt.Errorf("dataset tidak valid: butuh kolom CPU raw node2 (contoh: node2_cpu_raw_usage)")
	}
	cap1Idx, err := findHeader(index, "node1_cpu_capacity")
	if err != nil {
		return nil, err
	}
	cap2Idx, err := findHeader(index, "node2_cpu_capacity")
	if err != nil {
		return nil, err
	}
	queue1Idx, err := findHeader(index, "node1_queue")
	if err != nil {
		return nil, err
	}
	queue2Idx, err := findHeader(index, "node2_queue")
	if err != nil {
		return nil, err
	}
	resp1Idx, err := findHeader(index, "node1_response_ms", "node1_latency_ms")
	if err != nil {
		return nil, err
	}
	resp2Idx, err := findHeader(index, "node2_response_ms", "node2_latency_ms")
	if err != nil {
		return nil, err
	}
	osIdleIdx, _ := findHeader(index, "os_idle_cpu")
	tsIdx, _ := findHeader(index, "timestamp_utc", "timestamp")
	cpuUnitIdx, _ := findHeader(index, "cpu_usage_unit")

	out := make([]mopso.OfflineSample, 0, 1024)
	line := 1
	for {
		line++
		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}

		req1, err := parseIntCell(rec, req1Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node1_requests: %w", line, err)
		}
		req2, err := parseIntCell(rec, req2Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node2_requests: %w", line, err)
		}

		if cpuUnitIdx >= 0 {
			unit := strings.ToLower(strings.TrimSpace(rec[cpuUnitIdx]))
			if unit != "" && unit != "raw_percent_of_host" {
				return nil, fmt.Errorf("line %d: cpu_usage_unit=%q tidak didukung (harus raw_percent_of_host)", line, unit)
			}
		}

		cpu1, err := parseFloatCell(rec, cpu1Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node1_cpu_raw_usage: %w", line, err)
		}
		cpu2, err := parseFloatCell(rec, cpu2Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node2_cpu_raw_usage: %w", line, err)
		}
		cap1, err := parseFloatCell(rec, cap1Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node1_cpu_capacity: %w", line, err)
		}
		cap2, err := parseFloatCell(rec, cap2Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node2_cpu_capacity: %w", line, err)
		}
		q1, err := parseFloatCell(rec, queue1Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node1_queue: %w", line, err)
		}
		q2, err := parseFloatCell(rec, queue2Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node2_queue: %w", line, err)
		}
		rt1, err := parseFloatCell(rec, resp1Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node1_response_ms: %w", line, err)
		}
		rt2, err := parseFloatCell(rec, resp2Idx)
		if err != nil {
			return nil, fmt.Errorf("line %d: node2_response_ms: %w", line, err)
		}

		osIdle := 10.0
		if osIdleIdx >= 0 {
			v, parseErr := parseFloatCell(rec, osIdleIdx)
			if parseErr == nil {
				osIdle = v
			}
		}

		ts := ""
		if tsIdx >= 0 && tsIdx < len(rec) {
			ts = strings.TrimSpace(rec[tsIdx])
		}

		out = append(out, mopso.OfflineSample{
			Timestamp: ts,
			OSIdleCPU: osIdle,
			Node1: mopso.NodeState{
				CPUUsage:     cpu1,
				CPUCapacity:  cap1,
				QueueLength:  q1,
				ResponseTime: rt1,
				Requests:     req1,
			},
			Node2: mopso.NodeState{
				CPUUsage:     cpu2,
				CPUCapacity:  cap2,
				QueueLength:  q2,
				ResponseTime: rt2,
				Requests:     req2,
			},
		})
	}

	if len(out) == 0 {
		return nil, errors.New("dataset tidak memiliki row data")
	}
	return out, nil
}

func loadBaseParams(path string) ([]float64, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return append([]float64(nil), defaultBaseParams...), nil
		}
		return nil, err
	}

	var params []float64
	if err := json.Unmarshal(buf, &params); err != nil {
		return nil, err
	}
	if len(params) != mopso.Dimensions {
		return nil, fmt.Errorf("panjang base params harus %d, dapat %d", mopso.Dimensions, len(params))
	}
	return params, nil
}

func saveJSON(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func mapHeaderIndex(header []string) map[string]int {
	idx := make(map[string]int, len(header))
	for i := range header {
		idx[strings.ToLower(strings.TrimSpace(header[i]))] = i
	}
	return idx
}

func findHeader(index map[string]int, names ...string) (int, error) {
	for i := range names {
		if idx, ok := index[strings.ToLower(strings.TrimSpace(names[i]))]; ok {
			return idx, nil
		}
	}
	if len(names) == 0 {
		return -1, errors.New("nama header kosong")
	}
	if len(names) == 1 {
		return -1, fmt.Errorf("header %q tidak ditemukan", names[0])
	}
	return -1, fmt.Errorf("header %q tidak ditemukan", strings.Join(names, " / "))
}

func parseFloatCell(rec []string, index int) (float64, error) {
	if index < 0 || index >= len(rec) {
		return 0, errors.New("kolom tidak tersedia")
	}
	raw := strings.TrimSpace(rec[index])
	if raw == "" {
		return 0, errors.New("nilai kosong")
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func parseIntCell(rec []string, index int) (int64, error) {
	if index < 0 || index >= len(rec) {
		return 0, errors.New("kolom tidak tersedia")
	}
	raw := strings.TrimSpace(rec[index])
	if raw == "" {
		return 0, errors.New("nilai kosong")
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func absPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
