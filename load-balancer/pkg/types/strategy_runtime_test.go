package types

import "testing"

func TestUpdateResponseMSAllowsZero(t *testing.T) {
	node := &BackendNode{}

	node.UpdateResponseMS(123)
	node.UpdateResponseMS(0)

	snap := node.SnapshotForDecision()
	if snap.RespMS != 0 {
		t.Fatalf("RespMS = %v, want 0", snap.RespMS)
	}
	if node.LastRTObservedAt().IsZero() {
		t.Fatalf("LastRTObservedAt should be set when RespMS is updated to 0")
	}
}

func TestResetRuntimeState(t *testing.T) {
	node := &BackendNode{}
	node.UpdateMetrics(55, 7, 230, 100)
	node.RequestCount.Store(19)
	node.ProxyInflight.Store(6)
	node.SetLastProxyLatencyMS(230)

	node.ResetRuntimeState()

	snap := node.SnapshotForDecision()
	if snap.CPU != 0 {
		t.Fatalf("CPU = %v, want 0", snap.CPU)
	}
	if snap.RespMS != 0 {
		t.Fatalf("RespMS = %v, want 0", snap.RespMS)
	}
	if snap.Queue != 0 {
		t.Fatalf("Queue = %v, want 0", snap.Queue)
	}
	if snap.Inflight != 0 {
		t.Fatalf("Inflight = %v, want 0", snap.Inflight)
	}
	if node.RequestCount.Load() != 0 {
		t.Fatalf("RequestCount = %d, want 0", node.RequestCount.Load())
	}
	if node.ProxyInflight.Load() != 0 {
		t.Fatalf("ProxyInflight = %d, want 0", node.ProxyInflight.Load())
	}
	if node.LastProxyLatencyMS() != 0 {
		t.Fatalf("LastProxyLatencyMS = %v, want 0", node.LastProxyLatencyMS())
	}
	if !node.LastCPUObservedAt().IsZero() {
		t.Fatalf("LastCPUObservedAt should be zero after reset")
	}
	if !node.LastRTObservedAt().IsZero() {
		t.Fatalf("LastRTObservedAt should be zero after reset")
	}
}
