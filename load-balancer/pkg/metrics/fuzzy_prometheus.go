package metrics

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	fuzzyInputCPUPercent = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fuzzy_input_cpu_percent",
			Help: "CPU input (%) yang dipakai engine fuzzy per node.",
		},
		[]string{"node"},
	)
	fuzzyInputRtEmaMS = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fuzzy_input_rt_ema_ms",
			Help: "Response time EMA (ms) yang dipakai engine fuzzy per node.",
		},
		[]string{"node"},
	)
	fuzzyInputInflightQueue = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fuzzy_input_inflight_queue",
			Help: "Nilai queue/inflight real-time yang dipakai engine fuzzy per node.",
		},
		[]string{"node"},
	)
	fuzzyOutputScore = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fuzzy_output_score",
			Help: "Skor output fuzzy per node untuk keputusan routing.",
		},
		[]string{"node"},
	)

	registerFuzzyMetricsOnce sync.Once
)

func init() {
	registerFuzzyMetricsOnce.Do(func() {
		prometheus.MustRegister(fuzzyInputCPUPercent)
		prometheus.MustRegister(fuzzyInputRtEmaMS)
		prometheus.MustRegister(fuzzyInputInflightQueue)
		prometheus.MustRegister(fuzzyOutputScore)

		nodes := []string{"fmopso-stack_api-node1", "fmopso-stack_api-node2"}
		for _, n := range nodes {
			fuzzyInputCPUPercent.WithLabelValues(n).Set(0)
			fuzzyInputRtEmaMS.WithLabelValues(n).Set(0)
			fuzzyInputInflightQueue.WithLabelValues(n).Set(0)
			fuzzyOutputScore.WithLabelValues(n).Set(0)
		}
	})
}

func NodeLabel(nodeName string) string {
	n := strings.TrimSpace(nodeName)
	if n == "" {
		return n
	}
	if strings.Contains(n, "_") {
		return n
	}
	stack := strings.TrimSpace(os.Getenv("SWARM_STACK_NAME"))
	if stack == "" {
		stack = "fmopso-stack"
	}
	return fmt.Sprintf("%s_%s", stack, n)
}

func SetFuzzyInputCPU(node string, value float64) {
	fuzzyInputCPUPercent.WithLabelValues(node).Set(value)
}

func SetFuzzyInputRTEma(node string, value float64) {
	fuzzyInputRtEmaMS.WithLabelValues(node).Set(value)
}

func SetFuzzyInputInflight(node string, value float64) {
	fuzzyInputInflightQueue.WithLabelValues(node).Set(value)
}

func SetFuzzyOutputScore(node string, value float64) {
	fuzzyOutputScore.WithLabelValues(node).Set(value)
}
