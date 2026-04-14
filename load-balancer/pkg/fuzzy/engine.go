package fuzzy

import (
	"math"
	"sync"
)

// Engine menampung 27 parameter dinamis.
type Engine struct {
	mu     sync.RWMutex
	params []float64
}

// NewEngine membuat otak Fuzzy baru (Statis maupun Dinamis).
func NewEngine(initialParams []float64) *Engine {
	p := make([]float64, len(initialParams))
	copy(p, initialParams)
	return &Engine{params: p}
}

// UpdateParams dipanggil oleh optimizer (PSO/MOPSO) untuk memperbarui parameter.
func (e *Engine) UpdateParams(newParams []float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.params) != len(newParams) {
		e.params = make([]float64, len(newParams))
	}
	copy(e.params, newParams)
}

// GetParams mengembalikan snapshot parameter saat ini.
func (e *Engine) GetParams() []float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p := make([]float64, len(e.params))
	copy(p, e.params)
	return p
}

func (e *Engine) snapshotParams() []float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p := make([]float64, len(e.params))
	copy(p, e.params)
	return p
}

var MF_out = map[string]Triple{
	"Rendah": {0, 25, 50},
	"Sedang": {25, 50, 75},
	"Tinggi": {50, 75, 100},
}

// CalculateMamdani menggunakan snapshot immutable parameter agar hot-reload optimizer tidak memblokir request path.
func (e *Engine) CalculateMamdani(node NodeMetrics, rules []Rule) float64 {
	params := e.snapshotParams()

	muCPU := [3]float64{
		Fuzzify(node.CPU, Triple{params[0], params[1], params[2]}),
		Fuzzify(node.CPU, Triple{params[3], params[4], params[5]}),
		Fuzzify(node.CPU, Triple{params[6], params[7], params[8]}),
	}
	muQueue := [3]float64{
		Fuzzify(node.QueueLength, Triple{params[9], params[10], params[11]}),
		Fuzzify(node.QueueLength, Triple{params[12], params[13], params[14]}),
		Fuzzify(node.QueueLength, Triple{params[15], params[16], params[17]}),
	}
	muResp := [3]float64{
		Fuzzify(node.RespTime, Triple{params[18], params[19], params[20]}),
		Fuzzify(node.RespTime, Triple{params[21], params[22], params[23]}),
		Fuzzify(node.RespTime, Triple{params[24], params[25], params[26]}),
	}

	alphaOut := [3]float64{}
	for _, r := range rules {
		c := labelToIndex(r.CPULabel)
		q := labelToIndex(r.QueueLabel)
		rr := respLabelToIndex(r.RespLabel)
		o := labelToIndex(r.OutputLabel)
		if c < 0 || q < 0 || rr < 0 || o < 0 {
			continue
		}
		alpha := math.Min(muCPU[c], math.Min(muQueue[q], muResp[rr]))
		if alpha > alphaOut[o] {
			alphaOut[o] = alpha
		}
	}

	triangles := [3]Triple{{0, 25, 50}, {25, 50, 75}, {50, 75, 100}}
	var aTotal, mTotal float64
	for i, alpha := range alphaOut {
		if alpha <= 0 {
			continue
		}
		t := triangles[i]
		area := alpha * (t.C - t.A) / 2
		moment := area * (t.A + t.B + t.C) / 3
		aTotal += area
		mTotal += moment
	}

	if aTotal == 0 {
		return 0
	}
	return mTotal / aTotal
}

func labelToIndex(label string) int {
	switch label {
	case "Rendah":
		return 0
	case "Sedang":
		return 1
	case "Tinggi":
		return 2
	default:
		return -1
	}
}

func respLabelToIndex(label string) int {
	switch label {
	case "Cepat":
		return 0
	case "Normal":
		return 1
	case "Lambat":
		return 2
	default:
		return -1
	}
}

// CalculateMamdani mempertahankan kompatibilitas API lama (Pure Fuzzy default).
func CalculateMamdani(node NodeMetrics, rules []Rule) float64 {
	defaultParams := []float64{
		0, 0, 50, 0, 50, 100, 50, 100, 100,
		0, 0, 500, 0, 500, 1000, 500, 1000, 1000,
		0, 0, 500, 0, 500, 1000, 500, 1000, 1000,
	}
	engine := NewEngine(defaultParams)
	return engine.CalculateMamdani(node, rules)
}
