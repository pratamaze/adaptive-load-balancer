package fuzzy

import (
	"math"
	"sync"
)

// Engine menampung 27 parameter dinamis.
type Engine struct {
	mu       sync.RWMutex
	params   []float64
	outputMF [3]Triple
}

// NewEngine membuat otak Fuzzy baru (Statis maupun Dinamis).
func NewEngine(initialParams []float64, outputMF [3]Triple) *Engine {
	p := make([]float64, len(initialParams))
	copy(p, initialParams)
	return &Engine{
		params:   p,
		outputMF: outputMF,
	}
}

// UpdateParams dipanggil oleh optimizer (PSO/MOPSO) untuk memperbarui parameter.
func (e *Engine) UpdateParams(newParams []float64) {
	const alpha = 0.15

	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.params) != len(newParams) {
		e.params = make([]float64, len(newParams))
		copy(e.params, newParams)
		return
	}

	// EMA smoothing mencegah osilasi agresif saat parameter hot-reload.
	// p_t = alpha * p_baru + (1-alpha) * p_lama
	for i := range newParams {
		e.params[i] = (alpha * newParams[i]) + ((1 - alpha) * e.params[i])
	}
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

// CalculateMamdani menggunakan snapshot immutable parameter agar hot-reload optimizer tidak memblokir request path.
func (e *Engine) CalculateMamdani(node NodeMetrics, rules []Rule) float64 {
	params := e.snapshotParams()

	muCPU := [3]float64{
		FuzzifyLeft(node.CPU, Triple{params[0], params[1], params[2]}),
		FuzzifyTriangle(node.CPU, Triple{params[3], params[4], params[5]}),
		FuzzifyRight(node.CPU, Triple{params[6], params[7], params[8]}),
	}
	muQueue := [3]float64{
		FuzzifyLeft(node.QueueLength, Triple{params[9], params[10], params[11]}),
		FuzzifyTriangle(node.QueueLength, Triple{params[12], params[13], params[14]}),
		FuzzifyRight(node.QueueLength, Triple{params[15], params[16], params[17]}),
	}
	muResp := [3]float64{
		FuzzifyLeft(node.RespTime, Triple{params[18], params[19], params[20]}),
		FuzzifyTriangle(node.RespTime, Triple{params[21], params[22], params[23]}),
		FuzzifyRight(node.RespTime, Triple{params[24], params[25], params[26]}),
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

	return DefuzzifyMamdani(alphaOut, e.outputMF)
}

// DefuzzifyMamdani menyamakan jalur Go dengan training Python:
// domain diskrit z=0..100, inferensi MIN untuk truncation, lalu union MAX.
func DefuzzifyMamdani(alphaOut [3]float64, outputMF [3]Triple) float64 {
	var numerator, denominator float64

	for zi := 0; zi <= 100; zi++ {
		z := float64(zi)
		unionArea := 0.0

		for i, alpha := range alphaOut {
			if alpha <= 0 {
				continue
			}
			clipped := math.Min(alpha, Fuzzify(z, outputMF[i]))
			if clipped > unionArea {
				unionArea = clipped
			}
		}

		numerator += z * unionArea
		denominator += unionArea
	}

	if denominator == 0 {
		return 0
	}
	return numerator / denominator
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
