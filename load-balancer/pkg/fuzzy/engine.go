package fuzzy

import "math"

// MF_out: Himpunan Fuzzy Output (Skor Kelayakan)
var MF_out = map[string]Triple{
	"Rendah": {0, 25, 50},
	"Sedang": {25, 50, 75},
	"Tinggi": {50, 75, 100},
}

// CalculateMamdani menghitung skor akhir node berdasarkan metrik
func CalculateMamdani(node NodeMetrics, rules []Rule) float64 {
	// Fuzzifikasi Inputs
	muCPU := GetCPULevel(node.CPU)
	muQueue := GetQueueLevel(node.QueueLength)
	muResp := GetRespLevel(node.RespTime)

	// Rule Evaluation (Cari Alpha-Cut menggunakan MIN)
	type alphaRule struct {
		alpha float64
		label string
	}
	var alphaRules []alphaRule

	for _, r := range rules {
		// Alpha = Min(μ_cpu, μ_queue, μ_resp)
		alpha := math.Min(muCPU[r.CPULabel],
			math.Min(muQueue[r.QueueLabel], muResp[r.RespLabel]))

		if alpha > 0 {
			alphaRules = append(alphaRules, alphaRule{alpha, r.OutputLabel})
		}
	}

	// Aggregation (Cari Alpha Maksimal untuk setiap Label Output)
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

	// Defuzzification (Moment / Area)
	var aTotal, mTotal float64
	for lbl, t := range MF_out {
		alpha := alphaOut[lbl]
		if alpha > 0 {
			// Rumus Langkah 20-21: Area & Moment (Simplified Mamdani)
			area := alpha * (t.C - t.A) / 2
			moment := area * (t.A + t.B + t.C) / 3
			aTotal += area
			mTotal += moment
		}
	}

	// 26: Return Skor Akhir
	if aTotal == 0 {
		return 0
	}
	return mTotal / aTotal
}
