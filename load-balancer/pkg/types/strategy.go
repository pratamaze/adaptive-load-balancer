package types

import (
	"math"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// BackendNode adalah representasi netral node backend untuk proses decision.
// Struct ini juga menyimpan runtime state agar package lain tidak perlu import package main.
type BackendNode struct {
	Name string
	URL  *url.URL `json:"-"`

	CPU      float64
	Queue    float64
	RespMS   float64
	CPUCap   float64
	Inflight float64

	RequestCount  atomic.Int64 `json:"-"`
	ProxyInflight atomic.Int64 `json:"-"`

	lastProxyLatencyBit atomic.Uint64
	lastCPUObservedAtNS atomic.Int64
	lastRTObservedAtNS  atomic.Int64
	mu                  sync.RWMutex
}

func (n *BackendNode) SnapshotForDecision() BackendNode {
	queue := float64(n.ProxyInflight.Load())
	n.mu.RLock()
	defer n.mu.RUnlock()
	return BackendNode{
		Name:     n.Name,
		CPU:      n.CPU,
		Queue:    queue,
		RespMS:   n.RespMS,
		CPUCap:   n.CPUCap,
		Inflight: n.Inflight,
	}
}

func (n *BackendNode) UpdateMetrics(cpu, inflight, responseMS, cpuCap float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.CPU = cpu
	n.Inflight = inflight
	n.RespMS = responseMS
	n.CPUCap = cpuCap
	now := time.Now().UnixNano()
	n.lastCPUObservedAtNS.Store(now)
	if responseMS >= 0 {
		n.lastRTObservedAtNS.Store(now)
	}
}

func (n *BackendNode) UpdateCPU(cpu, cpuCap float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.CPU = cpu
	if cpuCap > 0 {
		n.CPUCap = cpuCap
	}
	n.lastCPUObservedAtNS.Store(time.Now().UnixNano())
}

func (n *BackendNode) UpdateResponseMS(ms float64) {
	if ms < 0 {
		return
	}
	n.mu.Lock()
	n.RespMS = ms
	n.mu.Unlock()
	n.lastRTObservedAtNS.Store(time.Now().UnixNano())
}

func (n *BackendNode) SetLastProxyLatencyMS(ms float64) {
	if ms < 0 {
		return
	}
	n.lastProxyLatencyBit.Store(math.Float64bits(ms))
}

func (n *BackendNode) ResetRuntimeState() {
	if n == nil {
		return
	}

	n.mu.Lock()
	n.CPU = 0
	n.Queue = 0
	n.RespMS = 0
	n.Inflight = 0
	n.mu.Unlock()

	n.RequestCount.Store(0)
	n.ProxyInflight.Store(0)
	n.lastProxyLatencyBit.Store(math.Float64bits(0))
	n.lastCPUObservedAtNS.Store(0)
	n.lastRTObservedAtNS.Store(0)
}

func (n *BackendNode) LastProxyLatencyMS() float64 {
	return math.Float64frombits(n.lastProxyLatencyBit.Load())
}

func (n *BackendNode) LastCPUObservedAt() time.Time {
	ts := n.lastCPUObservedAtNS.Load()
	if ts <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ts)
}

func (n *BackendNode) LastRTObservedAt() time.Time {
	ts := n.lastRTObservedAtNS.Load()
	if ts <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ts)
}

// DecisionSnapshot adalah single source of truth untuk satu keputusan routing.
// Field dipertahankan kompatibel dengan pipeline CSV logging yang sudah ada.
type DecisionSnapshot struct {
	Node1Name       string
	Node2Name       string
	CPU1            float64
	CPU2            float64
	CPU1Normalized  float64
	CPU2Normalized  float64
	Q1              float64
	Q2              float64
	RT1             float64
	RT2             float64
	Score1          float64
	Score2          float64
	SelectedNode    string
	RouletteValue   float64
	Node1CPUCap     float64
	Node2CPUCap     float64
	Node1BackendQ   float64
	Node2BackendQ   float64
	DecisionTag     string
	TotalScore      float64
	DecisionTimeUTC string
	NodeSnapshots   []BackendNode `json:"-"`
}

// ResponseSample adalah event Data Plane saat satu request selesai diproses.
// Worker CSV memakai event ini untuk menghitung latency window per node.
type ResponseSample struct {
	NodeName  string
	LatencyMS float64
}

// BalancerStrategy mengenkapsulasi logika pemilihan node backend.
type BalancerStrategy interface {
	SelectNode(nodes []BackendNode, snapshot *DecisionSnapshot) *BackendNode
}
