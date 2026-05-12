package proxy

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"load-balancer/pkg/algorithm/roundrobin"
	"load-balancer/pkg/metrics"
	lbtypes "load-balancer/pkg/types"
)

const selectedNodeTraceHeader = "X-LB-Selected-Node"
const decisionAuditLogEnabled = true
const responseEMAAlpha = 0.2

type backendNodeContextKey struct{}

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

	rtMu     sync.RWMutex
	rtEMAByN map[string]float64
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
	lb.rtEMAByN = make(map[string]float64, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		if n == nil {
			continue
		}
		s := n.SnapshotForDecision()
		if s.RespMS > 0 {
			lb.rtEMAByN[n.Name] = s.RespMS
		}
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
		if node == nil {
			continue
		}
		nodeSnapshots = append(nodeSnapshots, node.SnapshotForDecision())
	}
	if len(nodeSnapshots) == 0 {
		return nil, nil
	}

	decisionSnapshot := newDecisionSnapshot(nodeSnapshots, decisionTag)
	selectedNode := p.activeStrategy.SelectNode(nodeSnapshots, decisionSnapshot)
	if selectedNode == nil {
		selectedNode = p.fallbackStrategy.SelectNode(nodeSnapshots, decisionSnapshot)
		if decisionSnapshot != nil {
			decisionSnapshot.DecisionTag = "ROUND_ROBIN_FALLBACK"
		}
	}
	if selectedNode == nil {
		return nil, decisionSnapshot
	}
	if decisionSnapshot != nil {
		assignDecisionFallback(decisionSnapshot, selectedNode, decisionTag)
		if strings.TrimSpace(decisionSnapshot.Node1Name) != "" {
			metrics.SetFuzzyOutputScore(metrics.NodeLabel(decisionSnapshot.Node1Name), decisionSnapshot.Score1)
		}
		if strings.TrimSpace(decisionSnapshot.Node2Name) != "" {
			metrics.SetFuzzyOutputScore(metrics.NodeLabel(decisionSnapshot.Node2Name), decisionSnapshot.Score2)
		}
	}
	emitDecisionAudit(decisionSnapshot)
	p.enqueueDecisionSnapshot(decisionSnapshot)
	nodeRef := p.nodeByName(selectedNode.Name)
	if nodeRef != nil {
		return nodeRef, decisionSnapshot
	}
	for _, n := range p.nodes {
		if n != nil {
			return n, decisionSnapshot
		}
	}
	return nil, decisionSnapshot
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
			backendNode := backendNodeFromContext(req.Context())
			if backendNode == nil {
				log.Println("Gagal memilih backend, tidak ada node tersedia.")
				return
			}

			originalHost := req.Host
			req.URL.Scheme = backendNode.URL.Scheme
			req.URL.Host = backendNode.URL.Host
			req.Header.Set(selectedNodeTraceHeader, backendNode.Name)
			req.Header.Set("X-Forwarded-Host", originalHost)
			req.Host = originalHost
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			selectedNode := strings.TrimSpace(r.Header.Get(selectedNodeTraceHeader))
			if selectedNode == "" && r.URL != nil {
				selectedNode = p.nodeNameByHost(r.URL.Host)
			}
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
	mux.HandleFunc("/lb/runtime", p.statusHTTPHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lb/runtime" {
			p.statusHTTPHandler(w, r)
			return
		}
		p.proxyHTTPHandler(w, r)
	})
	return mux
}

func withBackendNode(ctx context.Context, node *lbtypes.BackendNode) context.Context {
	if node == nil {
		return ctx
	}
	return context.WithValue(ctx, backendNodeContextKey{}, node)
}

func backendNodeFromContext(ctx context.Context) *lbtypes.BackendNode {
	if ctx == nil {
		return nil
	}
	node, ok := ctx.Value(backendNodeContextKey{}).(*lbtypes.BackendNode)
	if !ok {
		return nil
	}
	return node
}

func (p *LBProxy) proxyHTTPHandler(w http.ResponseWriter, r *http.Request) {
	backendNode, _ := p.selectBackend()
	if backendNode == nil || backendNode.URL == nil {
		http.Error(w, "Service tidak tersedia", http.StatusServiceUnavailable)
		return
	}

	backendNode.ProxyInflight.Add(1)
	metrics.SetFuzzyInputInflight(metrics.NodeLabel(backendNode.Name), float64(backendNode.ProxyInflight.Load()))
	backendNode.RequestCount.Add(1)
	start := time.Now()
	defer func() {
		next := backendNode.ProxyInflight.Add(-1)
		if next < 0 {
			backendNode.ProxyInflight.Store(0)
			next = 0
		}
		metrics.SetFuzzyInputInflight(metrics.NodeLabel(backendNode.Name), float64(next))
		rtMs := time.Since(start).Milliseconds()
		if rtMs > 0 {
			p.updateResponseEMA(backendNode, float64(rtMs))
		}
	}()

	r = r.WithContext(withBackendNode(r.Context(), backendNode))
	p.reverseProxy.ServeHTTP(w, r)
}

func (p *LBProxy) updateResponseEMA(node *lbtypes.BackendNode, sampleMS float64) {
	if node == nil || sampleMS <= 0 {
		return
	}
	p.rtMu.Lock()
	prev := p.rtEMAByN[node.Name]
	if prev <= 0 {
		prev = sampleMS
	}
	next := (responseEMAAlpha * sampleMS) + ((1 - responseEMAAlpha) * prev)
	p.rtEMAByN[node.Name] = next
	p.rtMu.Unlock()
	node.UpdateResponseMS(next)
	node.SetLastProxyLatencyMS(next)
	metrics.SetFuzzyInputRTEma(metrics.NodeLabel(node.Name), next)
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
		out.CPU1Normalized = lbtypes.CalculateNormalizedCPU(n1.CPU, n1.CPUCap)
		out.Q1 = n1.Queue
		out.RT1 = n1.RespMS
		out.Node1CPUCap = n1.CPUCap
		out.Node1BackendQ = n1.Queue
	}
	if len(out.NodeSnapshots) > 1 {
		n2 := out.NodeSnapshots[1]
		out.Node2Name = n2.Name
		out.CPU2 = n2.CPU
		out.CPU2Normalized = lbtypes.CalculateNormalizedCPU(n2.CPU, n2.CPUCap)
		out.Q2 = n2.Queue
		out.RT2 = n2.RespMS
		out.Node2CPUCap = n2.CPUCap
		out.Node2BackendQ = n2.Queue
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
	node.UpdateMetrics(
		100.0,
		snapshot.Inflight,
		99999.0,
		snapshot.CPUCap,
	)
}
