package dataset

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

	log.Printf("[DATASET] Recorder fuzzy aktif ke file %s", csvPath)

	var mu sync.Mutex
	for snapshot := range snapshots {
		if snapshot == nil {
			continue
		}
		record, ok := buildPerHitRecord(snapshot)
		if !ok {
			continue
		}
		mu.Lock()
		if _, err := writer.WriteString(strings.Join(record, ",") + "\n"); err != nil {
			log.Printf("[DATASET] Gagal tulis record: %v", err)
		}
		if err := writer.Flush(); err != nil {
			log.Printf("[DATASET] Gagal flush writer: %v", err)
		}
		if err := file.Sync(); err != nil {
			log.Printf("[DATASET] Gagal sync file: %v", err)
		}
		mu.Unlock()
	}
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

func buildPerHitRecord(snapshot *lbtypes.DecisionSnapshot) ([]string, bool) {
	node1Name := snapshot.Node1Name
	node2Name := snapshot.Node2Name
	if strings.TrimSpace(node1Name) == "" || strings.TrimSpace(node2Name) == "" {
		return nil, false
	}

	r1, r2 := int64(0), int64(0)
	selectedNode := strings.TrimSpace(snapshot.SelectedNode)
	switch selectedNode {
	case node1Name:
		r1 = 1
	case node2Name:
		r2 = 1
	default:
		return nil, false
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
		config.TrafficLogModePerHit,
		cpuUsageUnitRaw,
		node1Name,
		node2Name,
		strconv.FormatInt(r1, 10),
		strconv.FormatInt(r2, 10),
		strconv.FormatInt(r1+r2, 10),
		// Kolom raw dipertahankan untuk kompatibilitas dataset historis;
		// runtime kini hanya menyimpan CPU normalized.
		formatFloatCSV(snapshot.CPU1),
		formatFloatCSV(snapshot.CPU2),
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

	return record, true
}

func formatFloatCSV(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}
