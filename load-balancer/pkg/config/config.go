package config

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"load-balancer/pkg/algorithm/fuzzy"
	lbtypes "load-balancer/pkg/types"
)

const (
	baseFuzzyParamsPath = "configs/base_fuzzy_params.json"
	optimizedFuzzyPath  = "storage/optimized_fuzzy_params.json"

	TrafficLogModeWindow = "window"
	TrafficLogModePerHit = "per_hit"

	defaultNode1CPUCapacity = 100.0
	defaultNode2CPUCapacity = 50.0
)

var DefaultBaseFuzzyParams = []float64{
	0, 40, 75, 60, 80, 95, 85, 95, 100,
	0, 50, 150, 100, 250, 400, 300, 500, 1000,
	0, 150, 300, 200, 500, 800, 600, 850, 1000,
}

var DefaultRules = []fuzzy.Rule{
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Rendah", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Rendah"},
	{CPULabel: "Sedang", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Tinggi", QueueLabel: "Rendah", RespLabel: "Normal", OutputLabel: "Sedang"},
	{CPULabel: "Tinggi", QueueLabel: "Rendah", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Sedang", RespLabel: "Cepat", OutputLabel: "Sedang"},
	{CPULabel: "Tinggi", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Sedang", RespLabel: "Lambat", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Tinggi", RespLabel: "Cepat", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Tinggi", RespLabel: "Normal", OutputLabel: "Rendah"},
	{CPULabel: "Tinggi", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},
}

type FuzzyBootstrap struct {
	BaseEngine   *fuzzy.Engine
	MOPSOEngine  *fuzzy.Engine
	ParamProfile string
}

type RuntimeConfig struct {
	Algorithm              string
	TrafficLogMode         string
	MetricsInterval        time.Duration
	AlgoLogInterval        time.Duration
	ListenAddr             string
	BackendNodes           []*lbtypes.BackendNode
	DecisionSnapshotBuffer int
	Fuzzy                  FuzzyBootstrap
}

func Load() (RuntimeConfig, error) {
	ensureDirs("configs", "storage")

	algorithm := normalizeAlgorithm(envLower("LB_ALGORITHM", "fuzzy_base"))
	trafficLogMode := normalizeTrafficLogMode(envLower("TRAFFIC_LOG_MODE", TrafficLogModePerHit))
	backendNodes, err := initBackendNodes([]string{"http://api-node1:8080", "http://api-node2:8080"})
	if err != nil {
		return RuntimeConfig{}, err
	}
	fuzzyBootstrap := initializeFuzzyEngines(algorithm)

	return RuntimeConfig{
		Algorithm:              algorithm,
		TrafficLogMode:         trafficLogMode,
		MetricsInterval:        envDurationMS("METRICS_INTERVAL", 50*time.Millisecond),
		AlgoLogInterval:        envDurationMS("ALGO_STATUS_LOG_INTERVAL", 30*time.Second),
		ListenAddr:             ":8080",
		BackendNodes:           backendNodes,
		DecisionSnapshotBuffer: 2048,
		Fuzzy:                  fuzzyBootstrap,
	}, nil
}

func ensureDirs(paths ...string) {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			log.Fatalf("Gagal membuat direktori %s: %v", p, err)
		}
	}
}

func envLower(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return strings.ToLower(v)
}

func envDurationMS(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	ms, err := time.ParseDuration(raw)
	if err == nil {
		return ms
	}
	if n, convErr := time.ParseDuration(raw + "ms"); convErr == nil {
		return n
	}
	return fallback
}

func normalizeAlgorithm(value string) string {
	switch value {
	case "rr", "roundrobin":
		return "rr"
	case "fuzzy_base", "fuzzy":
		return "fuzzy_base"
	case "fuzzy_mopso", "fmopso":
		return "fuzzy_mopso"
	default:
		log.Printf("[WARNING] LB_ALGORITHM tidak valid (%s), fallback ke fuzzy_base", value)
		return "fuzzy_base"
	}
}

func normalizeTrafficLogMode(value string) string {
	switch value {
	case TrafficLogModeWindow, TrafficLogModePerHit:
		return value
	default:
		log.Printf("[WARNING] TRAFFIC_LOG_MODE tidak dikenal (%s), fallback ke %s", value, TrafficLogModeWindow)
		return TrafficLogModeWindow
	}
}

func initBackendNodes(backendDNS []string) ([]*lbtypes.BackendNode, error) {
	nodes := make([]*lbtypes.BackendNode, 0, len(backendDNS))
	for i, dns := range backendDNS {
		backendURL, err := url.Parse(dns)
		if err != nil {
			return nil, fmt.Errorf("gagal mem-parse URL backend %q: %w", dns, err)
		}
		nodeName := fmt.Sprintf("api-node%d", i+1)
		cpuCap := resolveNodeCPUCapacity(i, nodeName)
		nodes = append(nodes, &lbtypes.BackendNode{Name: nodeName, URL: backendURL, CPUCap: cpuCap})
	}
	return nodes, nil
}

func resolveNodeCPUCapacity(index int, nodeName string) float64 {
	defaultCap := defaultCPUCapacityForIndex(index)
	candidates := []string{
		fmt.Sprintf("NODE%d_CPU_LIMIT_PERCENT", index+1),
		fmt.Sprintf("%s_CPU_LIMIT_PERCENT", strings.ToUpper(strings.ReplaceAll(nodeName, "-", "_"))),
	}
	for _, key := range candidates {
		if value, ok := readCPUCapacityEnv(key); ok {
			return value
		}
	}
	// Fallback kompatibilitas: CPU_LIMIT_PERCENT tunggal dipakai untuk node1.
	if index == 0 {
		if value, ok := readCPUCapacityEnv("CPU_LIMIT_PERCENT"); ok {
			return value
		}
	}
	return defaultCap
}

func defaultCPUCapacityForIndex(index int) float64 {
	switch index {
	case 1:
		return defaultNode2CPUCapacity
	default:
		return defaultNode1CPUCapacity
	}
}

func readCPUCapacityEnv(key string) (float64, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	raw = strings.TrimSuffix(raw, "%")
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		log.Printf("[WARNING] %s=%q tidak valid. Lewati override CPU capacity.", key, raw)
		return 0, false
	}
	return value, true
}

func initializeFuzzyEngines(algorithm string) FuzzyBootstrap {
	ensureBaseParamsFile(baseFuzzyParamsPath, DefaultBaseFuzzyParams)
	baseParams := loadFloatArrayWithFallback(baseFuzzyParamsPath, DefaultBaseFuzzyParams, "base fuzzy params")
	baseParams = sanitizeFuzzyParams(baseParams)
	mopsoParams := loadFloatArrayWithFallback(optimizedFuzzyPath, baseParams, "optimized fuzzy params")
	mopsoParams = sanitizeFuzzyParams(mopsoParams)

	bootstrap := FuzzyBootstrap{
		BaseEngine:   fuzzy.NewEngine(baseParams),
		MOPSOEngine:  fuzzy.NewEngine(mopsoParams),
		ParamProfile: "BASE",
	}
	if algorithm == "fuzzy_mopso" {
		bootstrap.ParamProfile = "OPTIMIZED_MOPSO"
	}
	log.Printf(
		"[ENTRYPOINT][PARAM-SOURCE] ALGO=%s PROFILE=%s BASE_FILE=%s MOPSO_FILE=%s",
		algorithm,
		bootstrap.ParamProfile,
		baseFuzzyParamsPath,
		optimizedFuzzyPath,
	)
	return bootstrap
}

func ensureBaseParamsFile(filename string, defaults []float64) {
	if _, err := os.Stat(filename); err == nil {
		return
	}
	if err := saveJSONToFile(filename, defaults); err != nil {
		log.Printf("[WARNING] Gagal membuat base params file %s: %v", filename, err)
	}
}

func loadFloatArrayWithFallback(filename string, fallback []float64, label string) []float64 {
	data, err := os.ReadFile(filename)
	if err != nil {
		log.Printf("[WARNING] File %s tidak ditemukan. Menggunakan %s default.", filename, label)
		return append([]float64(nil), fallback...)
	}
	var out []float64
	if err := json.Unmarshal(data, &out); err != nil || len(out) != len(fallback) {
		log.Printf("[WARNING] Gagal membaca %s. Menggunakan %s default.", filename, label)
		return append([]float64(nil), fallback...)
	}
	return out
}

func saveJSONToFile(filename string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func sanitizeFuzzyParams(params []float64) []float64 {
	out := append([]float64(nil), params...)
	const eps = 1e-6
	for i := 0; i+2 < len(out); i += 3 {
		hi := 1000.0
		if i <= 6 {
			hi = 100
		}
		a := math.Max(0, math.Min(hi, out[i]))
		b := math.Max(0, math.Min(hi, out[i+1]))
		c := math.Max(0, math.Min(hi, out[i+2]))

		if a > b {
			a, b = b, a
		}
		if b > c {
			b, c = c, b
		}
		if a > b {
			a, b = b, a
		}
		if b < a+eps {
			b = a + eps
		}
		if c < b+eps {
			c = b + eps
		}
		if c > hi {
			c = hi
			if b > c-eps {
				b = c - eps
			}
			if b < a+eps {
				a = math.Max(0, b-eps)
			}
		}

		out[i] = a
		out[i+1] = b
		out[i+2] = c
	}

	for i := 0; i+8 < len(out); i += 9 {
		if out[i+2] < out[i+3] {
			mid := (out[i+2] + out[i+3]) / 2
			out[i+2] = mid
			out[i+3] = mid
		}
		if out[i+5] < out[i+6] {
			mid := (out[i+5] + out[i+6]) / 2
			out[i+5] = mid
			out[i+6] = mid
		}
	}
	return out
}
