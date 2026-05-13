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
	CPUPercent float64 `json:"cpu_percent"`
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

	attempt := 0
	for {
		attempt++
		successCount := updateAllMetrics(nodes, client, paramProfile)
		if successCount > 0 {
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
			"[METRIC] Initial pull belum sukses, retry dalam %s src=%s attempt=%d",
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
			updateAllMetrics(nodes, client, paramProfile)
		}
	}()
}

func updateAllMetrics(nodes []*lbtypes.BackendNode, client *http.Client, paramProfile string) int {
	var wg sync.WaitGroup
	var successCount atomic.Int32
	for _, node := range nodes {
		if node == nil {
			continue
		}
		wg.Add(1)
		go func(n *lbtypes.BackendNode) {
			defer wg.Done()
			if collectNodeCPUMetric(n, client, paramProfile) {
				successCount.Add(1)
			}
		}(node)
	}
	wg.Wait()
	return int(successCount.Load())
}

func collectNodeCPUMetric(node *lbtypes.BackendNode, client *http.Client, paramProfile string) bool {
	if node == nil || node.URL == nil || client == nil {
		return false
	}

	telemetryURL := strings.TrimRight(node.URL.String(), "/") + "/telemetry/cpu"
	resp, err := client.Get(telemetryURL)
	if err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal ambil telemetry CPU dari %s: %v", activeParamProfile(paramProfile), node.Name, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[METRIC][SRC=%s] Telemetry CPU %s mengembalikan status=%d", activeParamProfile(paramProfile), node.Name, resp.StatusCode)
		return false
	}

	var payload cpuTelemetryPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal decode telemetry CPU %s: %v", activeParamProfile(paramProfile), node.Name, err)
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

func activeParamProfile(profile string) string {
	p := strings.TrimSpace(profile)
	if p == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(p)
}
