package metrics

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lbtypes "load-balancer/pkg/types"
)

type cpuTelemetryPayload struct {
	CPUPercent      float64 `json:"cpu_percent"`
	SampledAtUnix   int64   `json:"sampled_at_unix"`
	SampledAtUnixMS int64   `json:"sampled_at_unix_ms"`
}

var firstMetricCollectedOnce sync.Once

func StartCollector(nodes []*lbtypes.BackendNode, interval time.Duration, paramProfile string) {
	if len(nodes) == 0 {
		return
	}
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}

	client := &http.Client{Timeout: 1200 * time.Millisecond}
	validNodes := 0
	for _, node := range nodes {
		if node != nil {
			validNodes++
		}
	}
	if validNodes == 0 {
		return
	}

	retryDelay := interval
	if retryDelay > time.Second {
		retryDelay = time.Second
	}
	if retryDelay < 200*time.Millisecond {
		retryDelay = 200 * time.Millisecond
	}

	cpuStaleAfter := maxDuration(1500*time.Millisecond, interval*3)
	cpuDecayWindow := maxDuration(6*time.Second, interval*12)
	telemetryFreshnessTTL := maxDuration(2500*time.Millisecond, interval*10)

	attempt := 0
	for {
		attempt++
		successCount := updateAllMetrics(nodes, client, paramProfile, cpuStaleAfter, cpuDecayWindow, telemetryFreshnessTTL)
		if successCount == validNodes {
			log.Printf(
				"[METRIC] Initial pull selesai (%d/%d node sukses) src=%s attempt=%d",
				successCount,
				validNodes,
				activeParamProfile(paramProfile),
				attempt,
			)
			break
		}
		log.Printf(
			"[METRIC] Initial pull belum lengkap (%d/%d node sukses), retry dalam %s src=%s attempt=%d",
			successCount,
			validNodes,
			retryDelay,
			activeParamProfile(paramProfile),
			attempt,
		)
		time.Sleep(retryDelay)
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			updateAllMetrics(nodes, client, paramProfile, cpuStaleAfter, cpuDecayWindow, telemetryFreshnessTTL)
		}
	}()
}

func updateAllMetrics(nodes []*lbtypes.BackendNode, client *http.Client, paramProfile string, cpuStaleAfter, cpuDecayWindow, telemetryFreshnessTTL time.Duration) int {
	var wg sync.WaitGroup
	var successCount atomic.Int32
	for _, node := range nodes {
		if node == nil {
			continue
		}
		wg.Add(1)
		go func(n *lbtypes.BackendNode) {
			defer wg.Done()
			if collectNodeCPUMetric(n, client, paramProfile, cpuStaleAfter, cpuDecayWindow, telemetryFreshnessTTL) {
				successCount.Add(1)
			}
		}(node)
	}
	wg.Wait()
	return int(successCount.Load())
}

func collectNodeCPUMetric(node *lbtypes.BackendNode, client *http.Client, paramProfile string, cpuStaleAfter, cpuDecayWindow, telemetryFreshnessTTL time.Duration) bool {
	if node == nil || node.URL == nil || client == nil {
		return false
	}

	telemetryURL := strings.TrimRight(node.URL.String(), "/") + "/telemetry/cpu"
	resp, err := client.Get(telemetryURL)
	if err != nil {
		publishCPUState(node, cpuStaleAfter, cpuDecayWindow)
		log.Printf("[METRIC][SRC=%s] Gagal ambil telemetry CPU dari %s: %v", activeParamProfile(paramProfile), node.Name, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		publishCPUState(node, cpuStaleAfter, cpuDecayWindow)
		log.Printf("[METRIC][SRC=%s] Telemetry CPU %s mengembalikan status=%d", activeParamProfile(paramProfile), node.Name, resp.StatusCode)
		return false
	}

	var payload cpuTelemetryPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		publishCPUState(node, cpuStaleAfter, cpuDecayWindow)
		log.Printf("[METRIC][SRC=%s] Gagal decode telemetry CPU %s: %v", activeParamProfile(paramProfile), node.Name, err)
		return false
	}
	sampledAt := telemetrySampleTime(payload)
	if sampledAt.IsZero() {
		publishCPUState(node, cpuStaleAfter, cpuDecayWindow)
		log.Printf("[METRIC][SRC=%s] Telemetry CPU %s belum punya sample valid", activeParamProfile(paramProfile), node.Name)
		return false
	}
	sampleAge := time.Since(sampledAt)
	if sampleAge < 0 {
		sampleAge = 0
	}
	if telemetryFreshnessTTL > 0 && sampleAge > telemetryFreshnessTTL {
		publishCPUState(node, cpuStaleAfter, cpuDecayWindow)
		log.Printf(
			"[METRIC][SRC=%s] Telemetry CPU %s stale age=%s ttl=%s",
			activeParamProfile(paramProfile),
			node.Name,
			sampleAge,
			telemetryFreshnessTTL,
		)
		return false
	}

	cpuPct := payload.CPUPercent
	if cpuPct < 0 {
		cpuPct = 0
	}
	if cpuPct > 100 {
		cpuPct = 100
	}

	nodeSnapshot := node.SnapshotForDecision()
	node.UpdateCPU(cpuPct, nodeSnapshot.CPUCap)
	publishCPUState(node, cpuStaleAfter, cpuDecayWindow)
	firstMetricCollectedOnce.Do(func() {
		firstAt := node.LastCPUObservedAt().UTC()
		log.Printf(
			"[AUDIT][FIRST_METRIC_OK] at=%s unix_ns=%d node=%s cpu_pct=%.2f src=%s",
			firstAt.Format(time.RFC3339Nano),
			firstAt.UnixNano(),
			node.Name,
			cpuPct,
			activeParamProfile(paramProfile),
		)
	})
	return true
}

func telemetrySampleTime(payload cpuTelemetryPayload) time.Time {
	if payload.SampledAtUnixMS > 0 {
		return time.UnixMilli(payload.SampledAtUnixMS)
	}
	if payload.SampledAtUnix > 0 {
		return time.Unix(payload.SampledAtUnix, 0)
	}
	return time.Time{}
}

func publishCPUState(node *lbtypes.BackendNode, staleAfter, decayWindow time.Duration) {
	if node == nil {
		return
	}

	snap := node.SnapshotForDecision()
	cpu := clampCPU(snap.CPU)
	lastObserved := node.LastCPUObservedAt()
	if !lastObserved.IsZero() {
		cpu = decayToBaseline(cpu, 0.0, time.Since(lastObserved), staleAfter, decayWindow)
	}

	SetFuzzyInputCPU(
		NodeLabel(node.Name),
		lbtypes.CalculateNormalizedCPU(cpu, snap.CPUCap),
	)
}

func decayToBaseline(value, baseline float64, age, staleAfter, decayWindow time.Duration) float64 {
	if value <= 0 {
		return baseline
	}
	if staleAfter <= 0 || age <= staleAfter {
		return value
	}
	elapsed := age - staleAfter
	if decayWindow <= 0 || elapsed >= decayWindow {
		return baseline
	}
	ratio := 1.0 - (float64(elapsed) / float64(decayWindow))
	if ratio < 0 {
		ratio = 0
	}
	return baseline + ((value - baseline) * ratio)
}

func maxDuration(a, b time.Duration) time.Duration {
	if a >= b {
		return a
	}
	return b
}

func clampCPU(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func activeParamProfile(profile string) string {
	p := strings.TrimSpace(profile)
	if p == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(p)
}
