package dataset

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"load-balancer/pkg/config"
	lbtypes "load-balancer/pkg/types"
)

const (
	cpuUsageUnitRaw = "raw_percent_of_host"
	osIdleCPU10     = 3.0
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

type perSecondAggregate struct {
	node1Name string
	node2Name string

	node1Req int64
	node2Req int64

	node1CPURawSum          float64
	node2CPURawSum          float64
	node1CPUNormalizedSum   float64
	node2CPUNormalizedSum   float64
	node1InflightSum        float64
	node2InflightSum        float64
	node1BackendInflightSum float64
	node2BackendInflightSum float64
	node1RTSum              float64
	node2RTSum              float64
	node1SampleN            int64
	node2SampleN            int64

	score1Sum float64
	score2Sum float64
	scoreN    int64

	lastSnapshot *lbtypes.DecisionSnapshot
}

func NewSnapshotChannel(buffer int) chan *lbtypes.DecisionSnapshot {
	if buffer <= 0 {
		buffer = 2048
	}
	return make(chan *lbtypes.DecisionSnapshot, buffer)
}

func StartWorker(csvPath string, snapshots <-chan *lbtypes.DecisionSnapshot, algorithm, trafficLogMode string) {
	if snapshots == nil {
		return
	}

	enabled := (algorithm == "fuzzy_base" || algorithm == "fuzzy_mopso") && trafficLogMode == config.TrafficLogModePerHit
	if !enabled {
		for range snapshots {
		}
		return
	}

	file, writer, err := initCSV(csvPath)
	if err != nil {
		log.Printf("[DATASET] Gagal inisialisasi CSV %s: %v", csvPath, err)
		for range snapshots {
		}
		return
	}
	defer file.Close()

	log.Printf("[DATASET] Recorder fuzzy agregasi 1 detik aktif ke file %s", csvPath)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	agg := &perSecondAggregate{}

	for {
		select {
		case snapshot, ok := <-snapshots:
			if !ok {
				flushAggregate(file, writer, agg)
				return
			}
			accumulateSnapshot(agg, snapshot)
		case <-ticker.C:
			flushAggregate(file, writer, agg)
		}
	}
}

func accumulateSnapshot(agg *perSecondAggregate, snapshot *lbtypes.DecisionSnapshot) {
	if agg == nil || snapshot == nil {
		return
	}

	if strings.TrimSpace(snapshot.Node1Name) != "" {
		agg.node1Name = snapshot.Node1Name
	}
	if strings.TrimSpace(snapshot.Node2Name) != "" {
		agg.node2Name = snapshot.Node2Name
	}

	selected := strings.TrimSpace(snapshot.SelectedNode)
	switch selected {
	case agg.node1Name:
		agg.node1Req++
		agg.node1CPURawSum += snapshot.CPU1
		agg.node1CPUNormalizedSum += snapshot.CPU1Normalized
		agg.node1InflightSum += snapshot.Q1
		agg.node1BackendInflightSum += snapshot.Node1BackendQ
		agg.node1RTSum += snapshot.RT1
		agg.node1SampleN++
	case agg.node2Name:
		agg.node2Req++
		agg.node2CPURawSum += snapshot.CPU2
		agg.node2CPUNormalizedSum += snapshot.CPU2Normalized
		agg.node2InflightSum += snapshot.Q2
		agg.node2BackendInflightSum += snapshot.Node2BackendQ
		agg.node2RTSum += snapshot.RT2
		agg.node2SampleN++
	}

	agg.score1Sum += snapshot.Score1
	agg.score2Sum += snapshot.Score2
	agg.scoreN++

	cp := *snapshot
	agg.lastSnapshot = &cp
}

func flushAggregate(file *os.File, writer *bufio.Writer, agg *perSecondAggregate) {
	if agg == nil || writer == nil || file == nil {
		return
	}
	if strings.TrimSpace(agg.node1Name) == "" || strings.TrimSpace(agg.node2Name) == "" || agg.lastSnapshot == nil {
		return
	}

	avgScore1 := 0.0
	avgScore2 := 0.0
	if agg.scoreN > 0 {
		denom := float64(agg.scoreN)
		avgScore1 = agg.score1Sum / denom
		avgScore2 = agg.score2Sum / denom
	}

	s := agg.lastSnapshot
	cpuCap1 := s.Node1CPUCap
	cpuCap2 := s.Node2CPUCap
	if cpuCap1 <= 0 {
		cpuCap1 = 100
	}
	if cpuCap2 <= 0 {
		cpuCap2 = 100
	}

	totalReq := agg.node1Req + agg.node2Req
	avgNode1CPU := averageByCount(agg.node1CPURawSum, agg.node1SampleN)
	avgNode2CPU := averageByCount(agg.node2CPURawSum, agg.node2SampleN)
	avgNode1CPUNorm := averageByCount(agg.node1CPUNormalizedSum, agg.node1SampleN)
	avgNode2CPUNorm := averageByCount(agg.node2CPUNormalizedSum, agg.node2SampleN)
	avgNode1Inflight := averageByCount(agg.node1InflightSum, agg.node1SampleN)
	avgNode2Inflight := averageByCount(agg.node2InflightSum, agg.node2SampleN)
	avgNode1BackendInflight := averageByCount(agg.node1BackendInflightSum, agg.node1SampleN)
	avgNode2BackendInflight := averageByCount(agg.node2BackendInflightSum, agg.node2SampleN)
	avgNode1RT := averageByCount(agg.node1RTSum, agg.node1SampleN)
	avgNode2RT := averageByCount(agg.node2RTSum, agg.node2SampleN)

	record := []string{
		time.Now().UTC().Format(time.RFC3339Nano),
		"1000",
		config.TrafficLogModePerHit,
		cpuUsageUnitRaw,
		agg.node1Name,
		agg.node2Name,
		strconv.FormatInt(agg.node1Req, 10),
		strconv.FormatInt(agg.node2Req, 10),
		strconv.FormatInt(totalReq, 10),
		// Kolom raw dipertahankan untuk kompatibilitas dataset historis.
		formatFloatCSV(avgNode1CPU),
		formatFloatCSV(avgNode2CPU),
		formatFloatCSV(avgNode1CPUNorm),
		formatFloatCSV(avgNode2CPUNorm),
		formatFloatCSV(cpuCap1),
		formatFloatCSV(cpuCap2),
		formatFloatCSV(avgNode1Inflight),
		formatFloatCSV(avgNode2Inflight),
		formatFloatCSV(avgNode1BackendInflight),
		formatFloatCSV(avgNode2BackendInflight),
		formatFloatCSV(avgNode1Inflight),
		formatFloatCSV(avgNode2Inflight),
		formatFloatCSV(avgNode1RT),
		formatFloatCSV(avgNode2RT),
		formatFloatCSV(avgScore1),
		formatFloatCSV(avgScore2),
		formatFloatCSV(osIdleCPU10),
	}

	if _, err := writer.WriteString(strings.Join(record, ",") + "\n"); err != nil {
		log.Printf("[DATASET] Gagal tulis record agregasi: %v", err)
		return
	}
	if err := writer.Flush(); err != nil {
		log.Printf("[DATASET] Gagal flush writer: %v", err)
		return
	}
	if err := file.Sync(); err != nil {
		log.Printf("[DATASET] Gagal sync file: %v", err)
		return
	}

	agg.node1Req = 0
	agg.node2Req = 0
	agg.node1CPURawSum = 0
	agg.node2CPURawSum = 0
	agg.node1CPUNormalizedSum = 0
	agg.node2CPUNormalizedSum = 0
	agg.node1InflightSum = 0
	agg.node2InflightSum = 0
	agg.node1BackendInflightSum = 0
	agg.node2BackendInflightSum = 0
	agg.node1RTSum = 0
	agg.node2RTSum = 0
	agg.node1SampleN = 0
	agg.node2SampleN = 0
	agg.score1Sum = 0
	agg.score2Sum = 0
	agg.scoreN = 0
}

func initCSV(csvPath string) (*os.File, *bufio.Writer, error) {
	if err := os.MkdirAll(filepath.Dir(csvPath), 0o755); err != nil {
		return nil, nil, err
	}

	file, err := os.OpenFile(csvPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}

	writer := bufio.NewWriter(file)
	if info.Size() == 0 {
		if _, err := writer.WriteString(strings.Join(fuzzyTrainingDataHeader, ",") + "\n"); err != nil {
			_ = file.Close()
			return nil, nil, err
		}
		if err := writer.Flush(); err != nil {
			_ = file.Close()
			return nil, nil, err
		}
	}

	return file, writer, nil
}

func formatFloatCSV(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}

func averageByCount(sum float64, n int64) float64 {
	if n <= 0 {
		return 0
	}
	return sum / float64(n)
}
