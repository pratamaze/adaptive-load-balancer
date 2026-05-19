package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"load-balancer/pkg/algorithm/fuzzy"
	lbtypes "load-balancer/pkg/types"
)

const (
	baseFuzzyParamsPath      = "configs/base_fuzzy_params.json"
	optimizedFuzzyParamsPath = "configs/optimized_fuzzy_params.json"
	outputFuzzyMFPath        = "configs/fuzzy_output_mf.json"

	TrafficLogModeWindow = "window"
	TrafficLogModePerHit = "per_hit"

	defaultNode1CPUCapacity = 100.0
	defaultNode2CPUCapacity = 50.0
)

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

	algorithm, err := parseAlgorithm(envLowerAliases([]string{"LB_ALGORITHM", "LB_ALGO"}, "fuzzy"))
	if err != nil {
		return RuntimeConfig{}, err
	}
	paramSource, err := parseFuzzyParamSource(envLower("FUZZY_PARAM_SOURCE", "base"))
	if err != nil {
		return RuntimeConfig{}, err
	}
	trafficLogMode, err := parseTrafficLogMode(envLower("TRAFFIC_LOG_MODE", TrafficLogModePerHit))
	if err != nil {
		return RuntimeConfig{}, err
	}
	metricsInterval, err := envDurationMSStrict("METRICS_INTERVAL", 50*time.Millisecond)
	if err != nil {
		return RuntimeConfig{}, err
	}
	algoLogInterval, err := envDurationMSStrict("ALGO_STATUS_LOG_INTERVAL", 30*time.Second)
	if err != nil {
		return RuntimeConfig{}, err
	}
	backendNodes, err := initBackendNodes([]string{"http://api-node1:8080", "http://api-node2:8080"})
	if err != nil {
		return RuntimeConfig{}, err
	}
	fuzzyBootstrap, err := initializeFuzzyEngines(algorithm, paramSource)
	if err != nil {
		return RuntimeConfig{}, err
	}

	return RuntimeConfig{
		Algorithm:              algorithm,
		TrafficLogMode:         trafficLogMode,
		MetricsInterval:        metricsInterval,
		AlgoLogInterval:        algoLogInterval,
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

func envLowerAliases(keys []string, fallback string) string {
	for _, key := range keys {
		v := strings.TrimSpace(os.Getenv(key))
		if v != "" {
			return strings.ToLower(v)
		}
	}
	return fallback
}

func envDurationMSStrict(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	if value, err := time.ParseDuration(raw); err == nil {
		return value, nil
	}
	if value, err := time.ParseDuration(raw + "ms"); err == nil {
		return value, nil
	}
	return 0, fmt.Errorf("%s=%q tidak valid", key, raw)
}

func parseAlgorithm(value string) (string, error) {
	switch value {
	case "rr", "roundrobin":
		return "rr", nil
	case "fuzzy_base", "fuzzy":
		return "fuzzy_base", nil
	case "fuzzy_mopso", "fmopso":
		return "fuzzy_mopso", nil
	default:
		return "", fmt.Errorf("LB_ALGORITHM tidak valid: %s", value)
	}
}

func parseTrafficLogMode(value string) (string, error) {
	switch value {
	case TrafficLogModeWindow, TrafficLogModePerHit:
		return value, nil
	default:
		return "", fmt.Errorf("TRAFFIC_LOG_MODE tidak dikenal: %s", value)
	}
}

func parseFuzzyParamSource(value string) (string, error) {
	switch value {
	case "base":
		return "base", nil
	case "optimized", "mopso":
		return "optimized", nil
	default:
		return "", fmt.Errorf("FUZZY_PARAM_SOURCE tidak dikenal: %s", value)
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

func initializeFuzzyEngines(algorithm, paramSource string) (FuzzyBootstrap, error) {
	baseParams, baseSHA, err := loadFloatArrayStrict(baseFuzzyParamsPath, fuzzy.ParamCount, "base fuzzy params")
	if err != nil {
		return FuzzyBootstrap{}, err
	}
	outputMF, outputSHA, err := loadOutputMFStrict(outputFuzzyMFPath)
	if err != nil {
		return FuzzyBootstrap{}, err
	}

	bootstrap := FuzzyBootstrap{
		BaseEngine:   fuzzy.NewEngine(baseParams, outputMF),
		MOPSOEngine:  fuzzy.NewEngine(baseParams, outputMF),
		ParamProfile: "BASE",
	}
	needOptimized := algorithm == "fuzzy_mopso" || paramSource == "optimized"
	if needOptimized {
		mopsoParams, mopsoSHA, loadErr := loadFloatArrayStrict(optimizedFuzzyParamsPath, fuzzy.ParamCount, "optimized fuzzy params")
		if loadErr != nil {
			return FuzzyBootstrap{}, fmt.Errorf("mode optimized aktif tetapi optimized params gagal dimuat: %w", loadErr)
		}
		bootstrap.MOPSOEngine = fuzzy.NewEngine(mopsoParams, outputMF)
		bootstrap.ParamProfile = "OPTIMIZED_MOPSO"
		logParamSnapshot("OPTIMIZED_MOPSO", "INPUT_PARAMS", optimizedFuzzyParamsPath, mopsoSHA, mopsoParams)
		logParamSnapshot("OPTIMIZED_MOPSO", "OUTPUT_MF", outputFuzzyMFPath, outputSHA, outputMF)
	}

	logParamSnapshot("BASE", "INPUT_PARAMS", baseFuzzyParamsPath, baseSHA, baseParams)
	logParamSnapshot("BASE", "OUTPUT_MF", outputFuzzyMFPath, outputSHA, outputMF)
	log.Printf(
		"[ENTRYPOINT][PARAM-SOURCE] ALGO=%s REQUESTED_SOURCE=%s PROFILE=%s BASE_FILE=%s MOPSO_FILE=%s OUTPUT_MF_FILE=%s",
		algorithm,
		paramSource,
		bootstrap.ParamProfile,
		baseFuzzyParamsPath,
		optimizedFuzzyParamsPath,
		outputFuzzyMFPath,
	)
	return bootstrap, nil
}

func loadFloatArrayStrict(filename string, wantLen int, label string) ([]float64, string, error) {
	data, sha, err := readFileStrict(filename, label)
	if err != nil {
		return nil, "", err
	}
	var out []float64
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, "", fmt.Errorf("%s %s tidak valid: %w", label, filename, err)
	}
	if len(out) != wantLen {
		return nil, "", fmt.Errorf("%s %s harus berisi %d angka, dapat %d", label, filename, wantLen, len(out))
	}
	return out, sha, nil
}

func loadOutputMFStrict(filename string) ([3]fuzzy.Triple, string, error) {
	data, sha, err := readFileStrict(filename, "output MF")
	if err != nil {
		return [3]fuzzy.Triple{}, "", err
	}
	outputMF, err := fuzzy.ParseOutputMFConfig(data)
	if err != nil {
		return [3]fuzzy.Triple{}, "", fmt.Errorf("output MF %s tidak valid: %w", filename, err)
	}
	return outputMF, sha, nil
}

func readFileStrict(filename, label string) ([]byte, string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, "", fmt.Errorf("%s file %s gagal dibaca: %w", label, filename, err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func logParamSnapshot(profile, kind, filename, sha string, payload any) {
	log.Printf(
		"[ENTRYPOINT][PARAM-SNAPSHOT] PROFILE=%s KIND=%s FILE=%s SHA256=%s DATA=%v",
		profile,
		kind,
		filename,
		sha,
		payload,
	)
}
