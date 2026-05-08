package types

import (
	"math"
	"net/url"
	"sync"
	"sync/atomic"
)

// BackendNode adalah representasi netral node backend untuk proses decision.
// Struct ini juga menyimpan runtime state agar package lain tidak perlu import package main.
type BackendNode struct {
	Name string
	URL  *url.URL `json:"-"`

	CPU      float64
	Queue    float64
	RespMS   float64
	CPURaw   float64
	CPUCap   float64
	Inflight float64
	LoadAvg  float64

	MemoryUsage float64

	RequestCount  atomic.Int64 `json:"-"`
	ProxyInflight int64        `json:"-"`

	lastProxyLatencyBit atomic.Uint64
	mu                  sync.RWMutex
}

func (n *BackendNode) SnapshotForDecision() BackendNode {
	queue := float64(atomic.LoadInt64(&n.ProxyInflight))
	n.mu.RLock()
	defer n.mu.RUnlock()
	return BackendNode{
		Name:        n.Name,
		CPU:         n.CPU,
		Queue:       queue,
		RespMS:      n.RespMS,
		CPURaw:      n.CPURaw,
		CPUCap:      n.CPUCap,
		Inflight:    n.Inflight,
		LoadAvg:     n.LoadAvg,
		MemoryUsage: n.MemoryUsage,
	}
}

func (n *BackendNode) UpdateMetrics(cpu, cpuRaw, loadAvg, inflight, memoryUsage, responseMS, cpuCap float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.CPU = cpu
	n.CPURaw = cpuRaw
	n.LoadAvg = loadAvg
	n.Inflight = inflight
	n.MemoryUsage = memoryUsage
	n.RespMS = responseMS
	n.CPUCap = cpuCap
}

func (n *BackendNode) SetLastProxyLatencyMS(ms float64) {
	if ms <= 0 {
		return
	}
	n.lastProxyLatencyBit.Store(math.Float64bits(ms))
}

func (n *BackendNode) LastProxyLatencyMS() float64 {
	return math.Float64frombits(n.lastProxyLatencyBit.Load())
}

// DecisionSnapshot adalah single source of truth untuk satu keputusan routing.
// Field dipertahankan kompatibel dengan pipeline CSV logging yang sudah ada.
type DecisionSnapshot struct {
	Node1Name       string
	Node2Name       string
	CPU1            float64
	CPU2            float64
	Q1              float64
	Q2              float64
	RT1             float64
	RT2             float64
	Score1          float64
	Score2          float64
	SelectedNode    string
	RouletteValue   float64
	Node1CPURaw     float64
	Node2CPURaw     float64
	Node1CPUCap     float64
	Node2CPUCap     float64
	Node1BackendQ   float64
	Node2BackendQ   float64
	Node1LoadAvg    float64
	Node2LoadAvg    float64
	DecisionTag     string
	TotalScore      float64
	DecisionTimeUTC string
	NodeSnapshots   []BackendNode `json:"-"`
}

// BalancerStrategy mengenkapsulasi logika pemilihan node backend.
type BalancerStrategy interface {
	SelectNode(nodes []BackendNode, snapshot *DecisionSnapshot) *BackendNode
}
