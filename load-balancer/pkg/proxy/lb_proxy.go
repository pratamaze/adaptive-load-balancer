package proxy

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"load-balancer/pkg/algorithm/roundrobin"
	lbtypes "load-balancer/pkg/types"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const selectedNodeTraceHeader = "X-LB-Selected-Node"
const requestStartTraceHeader = "X-LB-Request-Start-UnixNano"
const decisionAuditLogEnabled = true

type decisionSnapshotContextKey struct{}

type LBProxyConfig struct {
	Nodes             []*lbtypes.BackendNode
	Algorithm         string
	ParamProfile      string
	TrafficLogMode    string
	MetricsEvery      time.Duration
	AlgoLogInterval   time.Duration
	ActiveStrategy    lbtypes.BalancerStrategy
	DecisionSnapshotC chan *lbtypes.DecisionSnapshot
}

type LBProxy struct {
	nodes             []*lbtypes.BackendNode
	algorithm         string
	paramProfile      string
	trafficLogMode    string
	metricsEvery      time.Duration
	activeStrategy    lbtypes.BalancerStrategy
	fallbackStrategy  lbtypes.BalancerStrategy
	decisionSnapshotC chan *lbtypes.DecisionSnapshot

	reverseProxy *httputil.ReverseProxy
	mux          *http.ServeMux
}

func NewLBProxy(cfg LBProxyConfig) *LBProxy {
	lb := &LBProxy{
		nodes:             cfg.Nodes,
		algorithm:         cfg.Algorithm,
		paramProfile:      cfg.ParamProfile,
		trafficLogMode:    cfg.TrafficLogMode,
		metricsEvery:      cfg.MetricsEvery,
		activeStrategy:    cfg.ActiveStrategy,
		fallbackStrategy:  roundrobin.NewRoundRobinStrategy(),
		decisionSnapshotC: cfg.DecisionSnapshotC,
	}
	if lb.activeStrategy == nil {
		lb.activeStrategy = roundrobin.NewRoundRobinStrategy()
	}
	lb.reverseProxy = lb.newReverseProxy()
	lb.mux = lb.newHTTPMux()
	lb.startAlgoStatusLogger(cfg.AlgoLogInterval)
	return lb
}

func (p *LBProxy) Handler() http.Handler {
	return p.mux
}

func (p *LBProxy) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, p.mux)
}

func (p *LBProxy) selectBackend() (*lbtypes.BackendNode, *lbtypes.DecisionSnapshot) {
	if len(p.nodes) == 0 {
		return nil, nil
	}

	decisionTag := strings.ToUpper(p.algorithm)
	nodeSnapshots := make([]lbtypes.BackendNode, 0, len(p.nodes))
	for _, node := range p.nodes {
		nodeSnapshots = append(nodeSnapshots, node.SnapshotForDecision())
	}

	decisionSnapshot := newDecisionSnapshot(nodeSnapshots, decisionTag)
	selectedNode := p.activeStrategy.SelectNode(nodeSnapshots, decisionSnapshot)
	if selectedNode == nil {
		selectedNode = p.fallbackStrategy.SelectNode(nodeSnapshots, decisionSnapshot)
		if decisionSnapshot != nil {
			decisionSnapshot.DecisionTag = "ROUND_ROBIN_FALLBACK"
		}
	}
	selectedRuntimeNode := (*lbtypes.BackendNode)(nil)
	if selectedNode != nil {
		selectedRuntimeNode = p.nodeByName(selectedNode.Name)
	}
	if selectedNode != nil && selectedRuntimeNode != nil && decisionSnapshot != nil {
		assignDecisionFallback(decisionSnapshot, selectedNode, decisionTag)
	}
	emitDecisionAudit(decisionSnapshot)
	return selectedRuntimeNode, decisionSnapshot
}

func (p *LBProxy) enqueueDecisionSnapshot(snapshot *lbtypes.DecisionSnapshot) {
	if snapshot == nil || p.decisionSnapshotC == nil {
		return
	}
	select {
	case p.decisionSnapshotC <- snapshot:
	default:
		log.Printf("[DATASET] Channel CSV penuh, snapshot decision terlewati untuk node=%s", snapshot.SelectedNode)
	}
}

func (p *LBProxy) newReverseProxy() *httputil.ReverseProxy {
	customTransport := &http.Transport{
		MaxIdleConns:          10000,
		MaxIdleConnsPerHost:   5000,
		MaxConnsPerHost:       15000,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	proxy := &httputil.ReverseProxy{
		Transport: customTransport,
		Director: func(req *http.Request) {
			backendNode, decisionSnapshot := p.selectBackend()
			if backendNode == nil {
				log.Println("Gagal memilih backend, tidak ada node tersedia.")
				return
			}

			atomic.AddInt64(&backendNode.ProxyInflight, 1)
			backendNode.RequestCount.Add(1)
			p.enqueueDecisionSnapshot(decisionSnapshot)
			originalHost := req.Host
			req.URL.Scheme = backendNode.URL.Scheme
			req.URL.Host = backendNode.URL.Host
			*req = *req.WithContext(withDecisionSnapshot(req.Context(), decisionSnapshot))
			req.Header.Set(selectedNodeTraceHeader, backendNode.Name)
			req.Header.Set(requestStartTraceHeader, strconv.FormatInt(time.Now().UnixNano(), 10))
			req.Header.Set("X-Forwarded-Host", originalHost)
			req.Host = originalHost
		},
		ModifyResponse: func(res *http.Response) error {
			if res == nil || res.Request == nil || res.Request.URL == nil {
				return nil
			}
			selectedNode := strings.TrimSpace(res.Request.Header.Get(selectedNodeTraceHeader))
			if selectedNode == "" {
				selectedNode = p.nodeNameByHost(res.Request.URL.Host)
			}
			latencyMS := requestLatencyMSFromTraceHeader(res.Request)
			if node := p.nodeByName(selectedNode); node != nil && latencyMS > 0 {
				node.SetLastProxyLatencyMS(latencyMS)
			}
			p.finalizeProxyRequest(selectedNode, 0)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			selectedNode := strings.TrimSpace(r.Header.Get(selectedNodeTraceHeader))
			if selectedNode == "" && r.URL != nil {
				selectedNode = p.nodeNameByHost(r.URL.Host)
			}
			latencyMS := requestLatencyMSFromTraceHeader(r)
			p.finalizeProxyRequest(selectedNode, latencyMS)
			targetHost := "unknown-host"
			path := ""
			if r.URL != nil {
				if strings.TrimSpace(r.URL.Host) != "" {
					targetHost = r.URL.Host
				}
				path = r.URL.Path
				if r.URL.RawQuery != "" {
					path += "?" + r.URL.RawQuery
				}
			}
			log.Printf(
				"[PROXY-ERROR] Node: %s | TargetHost: %s | Method: %s | Path: %s | Error: %v",
				selectedNode,
				targetHost,
				r.Method,
				path,
				err,
			)
			http.Error(w, "Service tidak tersedia", http.StatusServiceUnavailable)
		},
	}
	return proxy
}

func (p *LBProxy) finalizeProxyRequest(selectedNode string, latencyMS float64) {
	node := p.nodeByName(selectedNode)
	if node == nil {
		return
	}
	next := atomic.AddInt64(&node.ProxyInflight, -1)
	if next < 0 {
		atomic.StoreInt64(&node.ProxyInflight, 0)
	}
	if latencyMS > 0 {
		node.SetLastProxyLatencyMS(latencyMS)
	}
}

func (p *LBProxy) nodeNameByHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "unknown-node"
	}
	for _, n := range p.nodes {
		if n == nil || n.URL == nil {
			continue
		}
		if strings.EqualFold(n.URL.Host, host) {
			return n.Name
		}
	}
	return "unknown-node"
}

func (p *LBProxy) nodeByName(name string) *lbtypes.BackendNode {
	target := strings.TrimSpace(name)
	if target == "" {
		return nil
	}
	for _, n := range p.nodes {
		if n == nil {
			continue
		}
		if n.Name == target {
			return n
		}
	}
	return nil
}

func (p *LBProxy) activeParamProfile() string {
	profile := strings.TrimSpace(p.paramProfile)
	if profile == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(profile)
}

func (p *LBProxy) runtimeStatus() map[string]any {
	return map[string]any{
		"algorithm":        p.algorithm,
		"param_profile":    p.activeParamProfile(),
		"traffic_log_mode": p.trafficLogMode,
		"metrics_interval": p.metricsEvery.String(),
		"timestamp_utc":    time.Now().UTC().Format(time.RFC3339),
	}
}

func (p *LBProxy) statusHTTPHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Gunakan method GET", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(p.runtimeStatus())
}

func (p *LBProxy) startAlgoStatusLogger(interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			log.Printf(
				"[RUNTIME][ALGO-STATUS] ALGO=%s PARAM_PROFILE=%s TRAFFIC_LOG_MODE=%s METRICS_INTERVAL=%s",
				p.algorithm,
				p.activeParamProfile(),
				p.trafficLogMode,
				p.metricsEvery,
			)
		}
	}()
}

func (p *LBProxy) newHTTPMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/lb/runtime", p.statusHTTPHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			promhttp.Handler().ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/lb/runtime" {
			p.statusHTTPHandler(w, r)
			return
		}
		p.reverseProxy.ServeHTTP(w, r)
	})
	return mux
}

func requestLatencyMSFromTraceHeader(req *http.Request) float64 {
	if req == nil {
		return 0
	}
	raw := strings.TrimSpace(req.Header.Get(requestStartTraceHeader))
	if raw == "" {
		return 0
	}
	startUnixNano, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || startUnixNano <= 0 {
		return 0
	}
	startTime := time.Unix(0, startUnixNano)
	elapsed := time.Since(startTime).Seconds() * 1000.0
	if elapsed <= 0 {
		return 0
	}
	return elapsed
}

func withDecisionSnapshot(ctx context.Context, snapshot *lbtypes.DecisionSnapshot) context.Context {
	if snapshot == nil {
		return ctx
	}
	return context.WithValue(ctx, decisionSnapshotContextKey{}, snapshot)
}

func newDecisionSnapshot(nodes []lbtypes.BackendNode, decisionTag string) *lbtypes.DecisionSnapshot {
	if len(nodes) == 0 {
		return nil
	}
	out := &lbtypes.DecisionSnapshot{
		DecisionTag:     decisionTag,
		RouletteValue:   -1,
		DecisionTimeUTC: time.Now().UTC().Format(time.RFC3339Nano),
		NodeSnapshots:   append([]lbtypes.BackendNode(nil), nodes...),
	}
	if len(out.NodeSnapshots) > 0 {
		n1 := out.NodeSnapshots[0]
		out.Node1Name = n1.Name
		out.CPU1 = n1.CPU
		out.Q1 = n1.Queue
		out.RT1 = n1.RespMS
		out.Node1CPURaw = n1.CPURaw
		out.Node1CPUCap = n1.CPUCap
		out.Node1BackendQ = n1.Inflight
		out.Node1LoadAvg = n1.LoadAvg
	}
	if len(out.NodeSnapshots) > 1 {
		n2 := out.NodeSnapshots[1]
		out.Node2Name = n2.Name
		out.CPU2 = n2.CPU
		out.Q2 = n2.Queue
		out.RT2 = n2.RespMS
		out.Node2CPURaw = n2.CPURaw
		out.Node2CPUCap = n2.CPUCap
		out.Node2BackendQ = n2.Inflight
		out.Node2LoadAvg = n2.LoadAvg
	}
	return out
}

func assignDecisionFallback(snapshot *lbtypes.DecisionSnapshot, node *lbtypes.BackendNode, tag string) {
	if snapshot == nil || node == nil {
		return
	}
	if strings.TrimSpace(snapshot.DecisionTag) == "" {
		snapshot.DecisionTag = tag
	}
	if strings.TrimSpace(snapshot.SelectedNode) == "" {
		snapshot.SelectedNode = node.Name
	}
}

func emitDecisionAudit(snapshot *lbtypes.DecisionSnapshot) {
	if !decisionAuditLogEnabled || snapshot == nil {
		return
	}
	winner := strings.TrimSpace(snapshot.SelectedNode)
	if winner == "" {
		winner = "-"
	}
	log.Printf(
		"[DECISION_AUDIT] | Q1:%.2f | Q2:%.2f | S1:%.4f | S2:%.4f | R:%.4f | Win:%s",
		snapshot.Q1,
		snapshot.Q2,
		snapshot.Score1,
		snapshot.Score2,
		snapshot.RouletteValue,
		winner,
	)
}

func ApplyMetricPenalty(node *lbtypes.BackendNode) {
	if node == nil {
		return
	}
	snapshot := node.SnapshotForDecision()
	cpuRaw := 100.0
	if snapshot.CPUCap > 0 {
		cpuRaw = snapshot.CPUCap
	}
	node.UpdateMetrics(
		100.0,
		cpuRaw,
		snapshot.LoadAvg,
		snapshot.Inflight,
		snapshot.MemoryUsage,
		99999.0,
		snapshot.CPUCap,
	)
}
