package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	lbtypes "load-balancer/pkg/types"
)

func TestDecayRTForDecisionKeepsRecentIdleLatency(t *testing.T) {
	u, err := url.Parse("http://api-node1:8080")
	if err != nil {
		t.Fatalf("url parse failed: %v", err)
	}
	node := &lbtypes.BackendNode{Name: "api-node1", URL: u, CPUCap: 100}
	node.UpdateResponseMS(321)
	p := NewLBProxy(LBProxyConfig{
		Nodes: []*lbtypes.BackendNode{
			node,
		},
		Algorithm: "fuzzy_base",
	})

	p.telemetryMu.Lock()
	p.nodeTelemetry["api-node1"] = nodeTelemetry{observedRTMS: 321}
	p.telemetryMu.Unlock()

	got := p.decayRTForDecision("api-node1", 123, time.Now())
	if got != 321 {
		t.Fatalf("recent RT = %v, want 321", got)
	}
}

func TestDecayRTForDecisionDecaysStaleLatency(t *testing.T) {
	u, err := url.Parse("http://api-node1:8080")
	if err != nil {
		t.Fatalf("url parse failed: %v", err)
	}
	node := &lbtypes.BackendNode{Name: "api-node1", URL: u, CPUCap: 100}
	node.UpdateResponseMS(321)
	p := NewLBProxy(LBProxyConfig{
		Nodes: []*lbtypes.BackendNode{node},
	})
	p.cpuStaleAfter = time.Millisecond
	p.cpuDecayWindow = time.Millisecond

	p.telemetryMu.Lock()
	p.nodeTelemetry["api-node1"] = nodeTelemetry{observedRTMS: 321}
	p.telemetryMu.Unlock()

	time.Sleep(5 * time.Millisecond)

	got := p.decayRTForDecision("api-node1", 123, time.Now())
	if got != 0 {
		t.Fatalf("stale RT = %v, want 0", got)
	}
}

func TestResetTelemetryEndpoint(t *testing.T) {
	u, err := url.Parse("http://api-node1:8080")
	if err != nil {
		t.Fatalf("url parse failed: %v", err)
	}
	node := &lbtypes.BackendNode{Name: "api-node1", URL: u, CPUCap: 100}
	node.UpdateCPU(61, 100)
	node.UpdateResponseMS(440)
	node.ProxyInflight.Store(3)
	node.RequestCount.Store(9)
	node.SetLastProxyLatencyMS(440)

	p := NewLBProxy(LBProxyConfig{
		Nodes:     []*lbtypes.BackendNode{node},
		Algorithm: "fuzzy_base",
	})
	p.telemetryMu.Lock()
	p.nodeTelemetry["api-node1"] = nodeTelemetry{observedRTMS: 440}
	p.telemetryMu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/lb/reset-telemetry", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	snap := node.SnapshotForDecision()
	if snap.CPU != 0 || snap.RespMS != 0 || snap.Queue != 0 || snap.Inflight != 0 {
		t.Fatalf("node snapshot not reset: %+v", snap)
	}
	if node.RequestCount.Load() != 0 || node.ProxyInflight.Load() != 0 {
		t.Fatalf("atomic counters not reset: request=%d inflight=%d", node.RequestCount.Load(), node.ProxyInflight.Load())
	}

	p.telemetryMu.RLock()
	state := p.nodeTelemetry["api-node1"]
	p.telemetryMu.RUnlock()
	if state.observedRTMS != 0 {
		t.Fatalf("observedRTMS = %v, want 0", state.observedRTMS)
	}
}

func TestUpdateResponseRawEnqueuesResponseSample(t *testing.T) {
	u, err := url.Parse("http://api-node1:8080")
	if err != nil {
		t.Fatalf("url parse failed: %v", err)
	}
	node := &lbtypes.BackendNode{Name: "api-node1", URL: u, CPUCap: 100}
	responseSampleC := make(chan *lbtypes.ResponseSample, 1)
	p := NewLBProxy(LBProxyConfig{
		Nodes:           []*lbtypes.BackendNode{node},
		ResponseSampleC: responseSampleC,
	})

	p.updateResponseRaw(node, 187)

	select {
	case sample := <-responseSampleC:
		if sample == nil {
			t.Fatalf("response sample is nil")
		}
		if sample.NodeName != "api-node1" {
			t.Fatalf("NodeName = %s, want api-node1", sample.NodeName)
		}
		if sample.LatencyMS != 187 {
			t.Fatalf("LatencyMS = %v, want 187", sample.LatencyMS)
		}
	default:
		t.Fatalf("expected response sample to be enqueued")
	}
}
