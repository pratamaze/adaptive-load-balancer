package dataset

import (
	"strconv"
	"testing"
	"time"

	lbtypes "load-balancer/pkg/types"
)

func TestBuildCSVRecordUsesControlPlaneSnapshotForIdleNode(t *testing.T) {
	node1 := &lbtypes.BackendNode{Name: "api-node1", CPUCap: 100}
	node1.UpdateCPU(24, 100)
	node1.ProxyInflight.Store(5)

	node2 := &lbtypes.BackendNode{Name: "api-node2", CPUCap: 50}
	node2.UpdateCPU(18, 50)
	node2.ProxyInflight.Store(3)

	agg := &perSecondAggregate{
		node1Name:      "api-node1",
		node2Name:      "api-node2",
		node1Req:       7,
		node1RTSum:     1400,
		node1RTSamples: 7,
		score1Sum:      560,
		score2Sum:      420,
		scoreN:         7,
	}

	record := buildCSVRecord(time.Unix(0, 0).UTC(), captureNodeStates([]*lbtypes.BackendNode{node1, node2}, agg), agg)
	values := csvRecordMap(record)

	if values["node2_requests"] != "0" {
		t.Fatalf("node2_requests = %s, want 0", values["node2_requests"])
	}

	gotRawCPU2 := parseCSVFloat(t, values["node2_cpu_raw_usage"])
	if gotRawCPU2 != 18 {
		t.Fatalf("node2_cpu_raw_usage = %v, want 18", gotRawCPU2)
	}

	gotNormCPU2 := parseCSVFloat(t, values["node2_cpu_normalized_usage"])
	if gotNormCPU2 != 36 {
		t.Fatalf("node2_cpu_normalized_usage = %v, want 36", gotNormCPU2)
	}

	gotQueue2 := parseCSVFloat(t, values["node2_queue"])
	if gotQueue2 != 3 {
		t.Fatalf("node2_queue = %v, want 3", gotQueue2)
	}

	gotRT1 := parseCSVFloat(t, values["node1_response_ms"])
	if gotRT1 != 200 {
		t.Fatalf("node1_response_ms = %v, want 200", gotRT1)
	}

	gotRT2 := parseCSVFloat(t, values["node2_response_ms"])
	if gotRT2 != 0 {
		t.Fatalf("node2_response_ms = %v, want 0", gotRT2)
	}
}

func TestAccumulateSnapshotIgnoresDecisionLatencyAndUsesResponseSamples(t *testing.T) {
	agg := &perSecondAggregate{
		node1Name: "api-node1",
		node2Name: "api-node2",
	}

	accumulateSnapshot(agg, &lbtypes.DecisionSnapshot{
		Node1Name:    "api-node1",
		Node2Name:    "api-node2",
		SelectedNode: "api-node1",
		RT1:          999,
	})

	if agg.node1RTSum != 0 || agg.node1RTSamples != 0 {
		t.Fatalf("decision snapshot should not accumulate RT, got sum=%v count=%d", agg.node1RTSum, agg.node1RTSamples)
	}

	accumulateResponseSample(agg, &lbtypes.ResponseSample{
		NodeName:  "api-node1",
		LatencyMS: 215,
	})

	if agg.node1RTSum != 215 {
		t.Fatalf("node1RTSum = %v, want 215", agg.node1RTSum)
	}
	if agg.node1RTSamples != 1 {
		t.Fatalf("node1RTSamples = %d, want 1", agg.node1RTSamples)
	}
}

func csvRecordMap(record []string) map[string]string {
	out := make(map[string]string, len(record))
	for i, key := range fuzzyTrainingDataHeader {
		if i >= len(record) {
			break
		}
		out[key] = record[i]
	}
	return out
}

func parseCSVFloat(t *testing.T, raw string) float64 {
	t.Helper()
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("ParseFloat(%q) failed: %v", raw, err)
	}
	return value
}
