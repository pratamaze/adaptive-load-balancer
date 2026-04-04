package fuzzy

import (
	"math"
	"sync"
)

// Engine menampung 27 parameter dinamis
type Engine struct {
	mu     sync.RWMutex
	Params []float64
}

// NewEngine membuat otak Fuzzy baru (Statis maupun Dinamis)
func NewEngine(initialParams []float64) *Engine {
	p := make([]float64, len(initialParams))
	copy(p, initialParams)
	return &Engine{Params: p}
}

// UpdateParams dipanggil oleh PSO untuk memperbarui 27 angka
func (e *Engine) UpdateParams(newParams []float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	copy(e.Params, newParams)
}

// GetParams mengembalikan parameter saat ini
func (e *Engine) GetParams() []float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p := make([]float64, len(e.Params))
	copy(p, e.Params)
	return p
}

var MF_out = map[string]Triple{
	"Rendah": {0, 25, 50},
	"Sedang": {25, 50, 75},
	"Tinggi": {50, 75, 100},
}

// CalculateMamdani sekarang menjadi milik (e *Engine)
func (e *Engine) CalculateMamdani(node NodeMetrics, rules []Rule) float64 {
	// PANGGIL DARI DALAM ENGINE
	muCPU := e.GetCPULevel(node.CPU)
	muQueue := e.GetQueueLevel(node.QueueLength)
	muResp := e.GetRespLevel(node.RespTime)

	type alphaRule struct {
		alpha float64
		label string
	}
	var alphaRules []alphaRule

	for _, r := range rules {
		alpha := math.Min(muCPU[r.CPULabel], math.Min(muQueue[r.QueueLabel], muResp[r.RespLabel]))
		if alpha > 0 {
			alphaRules = append(alphaRules, alphaRule{alpha, r.OutputLabel})
		}
	}

	alphaOut := make(map[string]float64)
	for lbl := range MF_out {
		maxA := 0.0
		for _, ar := range alphaRules {
			if ar.label == lbl && ar.alpha > maxA {
				maxA = ar.alpha
			}
		}
		alphaOut[lbl] = maxA
	}

	var aTotal, mTotal float64
	for lbl, t := range MF_out {
		alpha := alphaOut[lbl]
		if alpha > 0 {
			area := alpha * (t.C - t.A) / 2
			moment := area * (t.A + t.B + t.C) / 3
			aTotal += area
			mTotal += moment
		}
	}

	if aTotal == 0 {
		return 0
	}
	return mTotal / aTotal
}
