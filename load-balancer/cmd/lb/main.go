package main

import (
	"log"
	"net/http"

	"load-balancer/pkg/algorithm/fuzzy"
	"load-balancer/pkg/algorithm/roundrobin"
	"load-balancer/pkg/config"
	"load-balancer/pkg/dataset"
	"load-balancer/pkg/metrics"
	"load-balancer/pkg/proxy"
	lbtypes "load-balancer/pkg/types"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Gagal load konfigurasi: %v", err)
	}

	var activeStrategy lbtypes.BalancerStrategy
	switch cfg.Algorithm {
	case "rr":
		activeStrategy = roundrobin.NewRoundRobinStrategy()
	case "fuzzy_mopso":
		activeStrategy = fuzzy.NewFuzzyMOPSOStrategy(cfg.Fuzzy.MOPSOEngine, config.DefaultRules)
	default:
		if cfg.Fuzzy.ParamProfile == "OPTIMIZED_MOPSO" {
			activeStrategy = fuzzy.NewFuzzyMOPSOStrategy(cfg.Fuzzy.MOPSOEngine, config.DefaultRules)
		} else {
			activeStrategy = fuzzy.NewFuzzyBaseStrategy(cfg.Fuzzy.BaseEngine, config.DefaultRules)
		}
	}

	decisionSnapshotC := dataset.NewSnapshotChannel(cfg.DecisionSnapshotBuffer)

	metrics.StartCollector(cfg.BackendNodes, cfg.MetricsInterval, cfg.Fuzzy.ParamProfile)
	go dataset.StartWorker("storage/fuzzy_training_data.csv", decisionSnapshotC, cfg.Algorithm, cfg.TrafficLogMode)
	go func() {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.Handler())
		const metricsAddr = ":9090"
		log.Printf("[METRICS] Prometheus endpoint aktif di %s/metrics", metricsAddr)
		if err := http.ListenAndServe(metricsAddr, metricsMux); err != nil {
			log.Fatalf("[METRICS] Gagal memulai endpoint metrics: %v", err)
		}
	}()

	lbProxy := proxy.NewLBProxy(proxy.LBProxyConfig{
		Nodes:             cfg.BackendNodes,
		Algorithm:         cfg.Algorithm,
		ParamProfile:      cfg.Fuzzy.ParamProfile,
		TrafficLogMode:    cfg.TrafficLogMode,
		MetricsEvery:      cfg.MetricsInterval,
		AlgoLogInterval:   cfg.AlgoLogInterval,
		ActiveStrategy:    activeStrategy,
		DecisionSnapshotC: decisionSnapshotC,
	})

	if cfg.TrafficLogMode != config.TrafficLogModePerHit {
		log.Printf("[DATASET] TRAFFIC_LOG_MODE=%s dinonaktifkan agar CSV hanya memakai DecisionSnapshot per-request", cfg.TrafficLogMode)
	}

	log.Printf(
		"Memulai Load Balancer di port %s (LB_ALGORITHM=%s, PARAM_PROFILE=%s, TRAFFIC_LOG_MODE=%s, METRICS_INTERVAL=%s)...",
		cfg.ListenAddr,
		cfg.Algorithm,
		cfg.Fuzzy.ParamProfile,
		cfg.TrafficLogMode,
		cfg.MetricsInterval,
	)

	if err := http.ListenAndServe(cfg.ListenAddr, lbProxy.Handler()); err != nil {
		log.Fatalf("Gagal memulai server: %v", err)
	}
}
