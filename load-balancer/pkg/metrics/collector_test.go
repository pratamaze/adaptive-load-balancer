package metrics

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	lbtypes "load-balancer/pkg/types"

	dto "github.com/prometheus/client_model/go"
)

func TestCollectNodeCPUMetricAcceptsFreshPayload(t *testing.T) {
	node := mustTestNode(t, "api-node1", "http://api-node1:8080")
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := fmt.Sprintf(`{"cpu_percent":27,"sampled_at_unix_ms":%d}`, time.Now().UnixMilli())
			return jsonResponse(http.StatusOK, body), nil
		}),
	}
	ok := collectNodeCPUMetric(node, client, "BASE", 100*time.Millisecond, time.Second, 250*time.Millisecond)
	if !ok {
		t.Fatal("collectNodeCPUMetric should accept fresh payload")
	}

	snap := node.SnapshotForDecision()
	if snap.CPU != 27 {
		t.Fatalf("node CPU = %v, want 27", snap.CPU)
	}

	gotGauge := readGaugeValue(t, fuzzyInputCPUPercent.WithLabelValues(NodeLabel(node.Name)))
	if gotGauge != 27 {
		t.Fatalf("CPU gauge = %v, want 27", gotGauge)
	}
}

func TestCollectNodeCPUMetricRejectsStalePayloadAndKeepsLastKnownCPU(t *testing.T) {
	node := mustTestNode(t, "api-node2", "http://api-node2:8080")
	node.UpdateCPU(64, 100)
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := fmt.Sprintf(`{"cpu_percent":12,"sampled_at_unix_ms":%d}`, time.Now().Add(-5*time.Second).UnixMilli())
			return jsonResponse(http.StatusOK, body), nil
		}),
	}

	ok := collectNodeCPUMetric(node, client, "BASE", 100*time.Millisecond, time.Second, 250*time.Millisecond)
	if ok {
		t.Fatal("collectNodeCPUMetric should reject stale payload")
	}

	snap := node.SnapshotForDecision()
	if snap.CPU != 64 {
		t.Fatalf("node CPU = %v, want last known 64", snap.CPU)
	}

	gotGauge := readGaugeValue(t, fuzzyInputCPUPercent.WithLabelValues(NodeLabel(node.Name)))
	if gotGauge != 64 {
		t.Fatalf("CPU gauge = %v, want 64", gotGauge)
	}
}

func TestPublishCPUStateDecaysGraduallyToBaseline(t *testing.T) {
	node := mustTestNode(t, "api-node3", "http://api-node3:8080")
	node.UpdateCPU(80, 100)

	publishCPUState(node, 10*time.Millisecond, 120*time.Millisecond)
	initial := readGaugeValue(t, fuzzyInputCPUPercent.WithLabelValues(NodeLabel(node.Name)))
	if initial != 80 {
		t.Fatalf("initial CPU gauge = %v, want 80", initial)
	}

	time.Sleep(60 * time.Millisecond)
	publishCPUState(node, 10*time.Millisecond, 120*time.Millisecond)
	mid := readGaugeValue(t, fuzzyInputCPUPercent.WithLabelValues(NodeLabel(node.Name)))
	if !(mid > 0 && mid < 80) {
		t.Fatalf("mid CPU gauge = %v, want gradual decay between 0 and 80", mid)
	}

	time.Sleep(120 * time.Millisecond)
	publishCPUState(node, 10*time.Millisecond, 120*time.Millisecond)
	final := readGaugeValue(t, fuzzyInputCPUPercent.WithLabelValues(NodeLabel(node.Name)))
	if final != 0 {
		t.Fatalf("final CPU gauge = %v, want 0 after full decay", final)
	}
}

func TestTelemetrySampleTimeFallsBackToSeconds(t *testing.T) {
	now := time.Now().Add(-time.Second).Truncate(time.Second)
	got := telemetrySampleTime(cpuTelemetryPayload{SampledAtUnix: now.Unix()})
	if !got.Equal(now) {
		t.Fatalf("telemetrySampleTime = %v, want %v", got, now)
	}
}

func mustTestNode(t *testing.T, name, rawURL string) *lbtypes.BackendNode {
	t.Helper()
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse error: %v", err)
	}
	return &lbtypes.BackendNode{
		Name:   name,
		URL:    parsedURL,
		CPUCap: 100,
	}
}

func readGaugeValue(t *testing.T, gauge interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := gauge.Write(metric); err != nil {
		t.Fatalf("gauge.Write error: %v", err)
	}
	if metric.Gauge == nil || metric.Gauge.Value == nil {
		t.Fatal("gauge value missing")
	}
	return metric.GetGauge().GetValue()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
