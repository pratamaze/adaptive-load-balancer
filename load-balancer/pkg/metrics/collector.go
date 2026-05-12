package metrics

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	lbtypes "load-balancer/pkg/types"
)

const metricsCollectInterval = 500 * time.Millisecond

type cpuTelemetryPayload struct {
	CPUPercent float64 `json:"cpu_percent"`
}

func StartCollector(nodes []*lbtypes.BackendNode, _ time.Duration, paramProfile string) {
	if len(nodes) == 0 {
		return
	}

	client := &http.Client{Timeout: 1200 * time.Millisecond}
	go func() {
		ticker := time.NewTicker(metricsCollectInterval)
		defer ticker.Stop()

		updateAllMetrics(nodes, client, paramProfile)
		for range ticker.C {
			updateAllMetrics(nodes, client, paramProfile)
		}
	}()
}

func updateAllMetrics(nodes []*lbtypes.BackendNode, client *http.Client, paramProfile string) {
	var wg sync.WaitGroup
	for _, node := range nodes {
		if node == nil {
			continue
		}
		wg.Add(1)
		go func(n *lbtypes.BackendNode) {
			defer wg.Done()
			collectNodeCPUMetric(n, client, paramProfile)
		}(node)
	}
	wg.Wait()
}

func collectNodeCPUMetric(node *lbtypes.BackendNode, client *http.Client, paramProfile string) {
	if node == nil || node.URL == nil || client == nil {
		return
	}

	telemetryURL := strings.TrimRight(node.URL.String(), "/") + "/telemetry/cpu"
	resp, err := client.Get(telemetryURL)
	if err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal ambil telemetry CPU dari %s: %v", activeParamProfile(paramProfile), node.Name, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[METRIC][SRC=%s] Telemetry CPU %s mengembalikan status=%d", activeParamProfile(paramProfile), node.Name, resp.StatusCode)
		return
	}

	var payload cpuTelemetryPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		log.Printf("[METRIC][SRC=%s] Gagal decode telemetry CPU %s: %v", activeParamProfile(paramProfile), node.Name, err)
		return
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
	normalizedCPU := lbtypes.CalculateNormalizedCPU(cpuPct, nodeSnapshot.CPUCap)

	labelNode := NodeLabel(node.Name)
	SetFuzzyInputCPU(labelNode, normalizedCPU)
}

func activeParamProfile(profile string) string {
	p := strings.TrimSpace(profile)
	if p == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(p)
}
