package fuzzy

import "math"

func Fuzzify(val float64, mf Triple) float64 {
	const eps = 1e-6

	// Left shoulder (a ~= b): membership penuh di sisi kiri.
	if math.Abs(mf.B-mf.A) <= eps {
		if val <= mf.B {
			return 1.0
		}
		if val >= mf.C {
			return 0.0
		}
		den := mf.C - mf.B
		if den <= eps {
			return 0
		}
		return (mf.C - val) / den
	}

	// Right shoulder (b ~= c): membership penuh di sisi kanan.
	if math.Abs(mf.C-mf.B) <= eps {
		if val >= mf.B {
			return 1.0
		}
		if val <= mf.A {
			return 0.0
		}
		den := mf.B - mf.A
		if den <= eps {
			return 0
		}
		return (val - mf.A) / den
	}

	if val == mf.B {
		return 1.0
	}
	if val <= mf.A || val >= mf.C {
		return 0.0
	}
	if val < mf.B {
		den := mf.B - mf.A
		if den == 0 {
			return 0
		}
		return (val - mf.A) / den
	}
	den := mf.C - mf.B
	if den == 0 {
		return 0
	}
	return (mf.C - val) / den
}

// Map Indeks: CPU (0-8)
func (e *Engine) GetCPULevel(val float64) map[string]float64 {
	params := e.GetParams()
	return map[string]float64{
		"Rendah": Fuzzify(val, Triple{params[0], params[1], params[2]}),
		"Sedang": Fuzzify(val, Triple{params[3], params[4], params[5]}),
		"Tinggi": Fuzzify(val, Triple{params[6], params[7], params[8]}),
	}
}

// Map Indeks: Queue (9-17)
func (e *Engine) GetQueueLevel(val float64) map[string]float64 {
	params := e.GetParams()
	return map[string]float64{
		"Rendah": Fuzzify(val, Triple{params[9], params[10], params[11]}),
		"Sedang": Fuzzify(val, Triple{params[12], params[13], params[14]}),
		"Tinggi": Fuzzify(val, Triple{params[15], params[16], params[17]}),
	}
}

// Map Indeks: Response Time (18-26)
func (e *Engine) GetRespLevel(val float64) map[string]float64 {
	params := e.GetParams()
	return map[string]float64{
		"Cepat":  Fuzzify(val, Triple{params[18], params[19], params[20]}),
		"Normal": Fuzzify(val, Triple{params[21], params[22], params[23]}),
		"Lambat": Fuzzify(val, Triple{params[24], params[25], params[26]}),
	}
}
