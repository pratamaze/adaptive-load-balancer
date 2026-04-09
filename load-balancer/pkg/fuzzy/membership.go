package fuzzy

func Fuzzify(val float64, mf Triple) float64 {
	if val == mf.B {
		return 1.0
	}
	if val <= mf.A || val >= mf.C {
		return 0.0
	}
	if val < mf.B {
		return (val - mf.A) / (mf.B - mf.A)
	}
	return (mf.C - val) / (mf.C - mf.B)
}

// Map Indeks: CPU (0-8)
func (e *Engine) GetCPULevel(val float64) map[string]float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return map[string]float64{
		"Rendah": Fuzzify(val, Triple{e.Params[0], e.Params[1], e.Params[2]}),
		"Sedang": Fuzzify(val, Triple{e.Params[3], e.Params[4], e.Params[5]}),
		"Tinggi": Fuzzify(val, Triple{e.Params[6], e.Params[7], e.Params[8]}),
	}
}

// Map Indeks: Queue (9-17)
func (e *Engine) GetQueueLevel(val float64) map[string]float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return map[string]float64{
		"Rendah": Fuzzify(val, Triple{e.Params[9], e.Params[10], e.Params[11]}),
		"Sedang": Fuzzify(val, Triple{e.Params[12], e.Params[13], e.Params[14]}),
		"Tinggi": Fuzzify(val, Triple{e.Params[15], e.Params[16], e.Params[17]}),
	}
}

// Map Indeks: Response Time (18-26)
func (e *Engine) GetRespLevel(val float64) map[string]float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return map[string]float64{
		"Cepat":  Fuzzify(val, Triple{e.Params[18], e.Params[19], e.Params[20]}),
		"Normal": Fuzzify(val, Triple{e.Params[21], e.Params[22], e.Params[23]}),
		"Lambat": Fuzzify(val, Triple{e.Params[24], e.Params[25], e.Params[26]}),
	}
}
