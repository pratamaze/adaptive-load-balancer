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
	recordWindow    = 1 * time.Second
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

// perSecondAggregate menyimpan metrik window Data Plane.
// Nilai CPU dan kapasitas sengaja tidak disimpan di sini agar worker
// selalu membaca state terbaru dari Control Plane saat ticker berdetak.
type perSecondAggregate struct {
	node1Name string
	node2Name string

	node1Req int64
	node2Req int64

	node1RTSum     float64
	node2RTSum     float64
	node1RTSamples int64
	node2RTSamples int64

	score1Sum float64
	score2Sum float64
	scoreN    int64
}

type nodeRuntimeSnapshot struct {
	name            string
	rawCPU          float64
	normalizedCPU   float64
	cpuCap          float64
	proxyInflight   float64
	backendInflight float64
	queue           float64
}

func NewSnapshotChannel(buffer int) chan *lbtypes.DecisionSnapshot {
	if buffer <= 0 {
		buffer = 2048
	}
	return make(chan *lbtypes.DecisionSnapshot, buffer)
}

func NewResponseSampleChannel(buffer int) chan *lbtypes.ResponseSample {
	if buffer <= 0 {
		buffer = 2048
	}
	return make(chan *lbtypes.ResponseSample, buffer)
}

func StartWorker(csvPath string, nodes []*lbtypes.BackendNode, snapshots <-chan *lbtypes.DecisionSnapshot, responseSamples <-chan *lbtypes.ResponseSample, algorithm, trafficLogMode string) {
	if snapshots == nil && responseSamples == nil {
		return
	}

	enabled := (algorithm == "fuzzy_base" || algorithm == "fuzzy_mopso") && trafficLogMode == config.TrafficLogModePerHit
	if !enabled {
		drainWorkerChannels(snapshots, responseSamples)
		return
	}

	file, writer, err := initCSV(csvPath)
	if err != nil {
		log.Printf("[DATASET] Gagal inisialisasi CSV %s: %v", csvPath, err)
		drainWorkerChannels(snapshots, responseSamples)
		return
	}
	defer file.Close()

	log.Printf("[DATASET] Recorder control-plane 1 detik aktif ke file %s", csvPath)

	ticker := time.NewTicker(recordWindow)
	defer ticker.Stop()

	agg := newPerSecondAggregate(nodes)

	for {
		if snapshots == nil && responseSamples == nil {
			flushAggregate(file, writer, nodes, agg, time.Now().UTC())
			return
		}
		select {
		case snapshot, ok := <-snapshots:
			if !ok {
				snapshots = nil
				continue
			}
			accumulateSnapshot(agg, snapshot)
		case sample, ok := <-responseSamples:
			if !ok {
				responseSamples = nil
				continue
			}
			accumulateResponseSample(agg, sample)
		case tickAt := <-ticker.C:
			flushAggregate(file, writer, nodes, agg, tickAt.UTC())
		}
	}
}

func drainWorkerChannels(snapshots <-chan *lbtypes.DecisionSnapshot, responseSamples <-chan *lbtypes.ResponseSample) {
	for snapshots != nil || responseSamples != nil {
		select {
		case _, ok := <-snapshots:
			if !ok {
				snapshots = nil
			}
		case _, ok := <-responseSamples:
			if !ok {
				responseSamples = nil
			}
		}
	}
}

func newPerSecondAggregate(nodes []*lbtypes.BackendNode) *perSecondAggregate {
	agg := &perSecondAggregate{}
	nodeRefs := firstTwoNodes(nodes)
	if nodeRefs[0] != nil {
		agg.node1Name = strings.TrimSpace(nodeRefs[0].Name)
	}
	if nodeRefs[1] != nil {
		agg.node2Name = strings.TrimSpace(nodeRefs[1].Name)
	}
	return agg
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
	case agg.node2Name:
		agg.node2Req++
	}

	agg.score1Sum += snapshot.Score1
	agg.score2Sum += snapshot.Score2
	agg.scoreN++
}

func accumulateResponseSample(agg *perSecondAggregate, sample *lbtypes.ResponseSample) {
	if agg == nil || sample == nil || sample.LatencyMS <= 0 {
		return
	}

	switch strings.TrimSpace(sample.NodeName) {
	case agg.node1Name:
		agg.node1RTSum += sample.LatencyMS
		agg.node1RTSamples++
	case agg.node2Name:
		agg.node2RTSum += sample.LatencyMS
		agg.node2RTSamples++
	}
}

func flushAggregate(file *os.File, writer *bufio.Writer, nodes []*lbtypes.BackendNode, agg *perSecondAggregate, tickAt time.Time) {
	if agg == nil || writer == nil || file == nil {
		return
	}

	nodeStates := captureNodeStates(nodes, agg)
	if strings.TrimSpace(nodeStates[0].name) == "" && strings.TrimSpace(nodeStates[1].name) == "" {
		return
	}

	record := buildCSVRecord(tickAt, nodeStates, agg)
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

	resetWindowAggregate(agg)
}

func firstTwoNodes(nodes []*lbtypes.BackendNode) [2]*lbtypes.BackendNode {
	var out [2]*lbtypes.BackendNode
	idx := 0
	for _, node := range nodes {
		if node == nil {
			continue
		}
		out[idx] = node
		idx++
		if idx == len(out) {
			break
		}
	}
	return out
}

func captureNodeStates(nodes []*lbtypes.BackendNode, agg *perSecondAggregate) [2]nodeRuntimeSnapshot {
	nodeRefs := firstTwoNodes(nodes)
	return [2]nodeRuntimeSnapshot{
		captureNodeState(nodeRefs[0], agg.node1Name),
		captureNodeState(nodeRefs[1], agg.node2Name),
	}
}

func captureNodeState(node *lbtypes.BackendNode, fallbackName string) nodeRuntimeSnapshot {
	state := nodeRuntimeSnapshot{
		name:   strings.TrimSpace(fallbackName),
		cpuCap: 100.0,
	}
	if node == nil {
		return state
	}

	snap := node.SnapshotForDecision()
	if strings.TrimSpace(node.Name) != "" {
		state.name = strings.TrimSpace(node.Name)
	}

	cpuCap := snap.CPUCap
	if cpuCap <= 0 {
		cpuCap = 100.0
	}
	rawCPU := clampPercent(snap.CPU)
	queue := clampNonNegative(snap.Queue)
	backendInflight := clampNonNegative(snap.Inflight)

	state.rawCPU = rawCPU
	state.cpuCap = cpuCap
	state.normalizedCPU = lbtypes.CalculateNormalizedCPU(rawCPU, cpuCap)
	state.proxyInflight = queue
	state.backendInflight = backendInflight
	state.queue = queue
	return state
}

func buildCSVRecord(tickAt time.Time, nodes [2]nodeRuntimeSnapshot, agg *perSecondAggregate) []string {
	if tickAt.IsZero() {
		tickAt = time.Now().UTC()
	}

	avgScore1 := 0.0
	avgScore2 := 0.0
	if agg.scoreN > 0 {
		denom := float64(agg.scoreN)
		avgScore1 = agg.score1Sum / denom
		avgScore2 = agg.score2Sum / denom
	}

	avgNode1RT := averageByCount(agg.node1RTSum, agg.node1RTSamples)
	avgNode2RT := averageByCount(agg.node2RTSum, agg.node2RTSamples)
	totalReq := agg.node1Req + agg.node2Req

	return []string{
		tickAt.Format(time.RFC3339Nano),
		strconv.FormatInt(recordWindow.Milliseconds(), 10),
		config.TrafficLogModePerHit,
		cpuUsageUnitRaw,
		nodes[0].name,
		nodes[1].name,
		strconv.FormatInt(agg.node1Req, 10),
		strconv.FormatInt(agg.node2Req, 10),
		strconv.FormatInt(totalReq, 10),
		formatFloatCSV(nodes[0].rawCPU),
		formatFloatCSV(nodes[1].rawCPU),
		formatFloatCSV(nodes[0].normalizedCPU),
		formatFloatCSV(nodes[1].normalizedCPU),
		formatFloatCSV(nodes[0].cpuCap),
		formatFloatCSV(nodes[1].cpuCap),
		formatFloatCSV(nodes[0].proxyInflight),
		formatFloatCSV(nodes[1].proxyInflight),
		formatFloatCSV(nodes[0].backendInflight),
		formatFloatCSV(nodes[1].backendInflight),
		formatFloatCSV(nodes[0].queue),
		formatFloatCSV(nodes[1].queue),
		formatFloatCSV(avgNode1RT),
		formatFloatCSV(avgNode2RT),
		formatFloatCSV(avgScore1),
		formatFloatCSV(avgScore2),
		formatFloatCSV(osIdleCPU10),
	}
}

func resetWindowAggregate(agg *perSecondAggregate) {
	if agg == nil {
		return
	}
	agg.node1Req = 0
	agg.node2Req = 0
	agg.node1RTSum = 0
	agg.node2RTSum = 0
	agg.node1RTSamples = 0
	agg.node2RTSamples = 0
	agg.score1Sum = 0
	agg.score2Sum = 0
	agg.scoreN = 0
}

func clampPercent(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func clampNonNegative(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return v
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
