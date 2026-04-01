package fuzzy

// Triangulas Membership function
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

// derajat CPU (0-100%)
func GetCPULevel(val float64) map[string]float64 {
	return map[string]float64{
		"Rendah": Fuzzify(val, Triple{0, 0, 50}),
		"Sedang": Fuzzify(val, Triple{0, 50, 100}),
		"Tinggi": Fuzzify(val, Triple{50, 100, 100}),
	}
}

// derajat Queue (0-1000 req)
func GetQueueLevel(val float64) map[string]float64 {
	return map[string]float64{
		"Rendah": Fuzzify(val, Triple{0, 0, 500}),
		"Sedang": Fuzzify(val, Triple{0, 500, 1000}),
		"Tinggi": Fuzzify(val, Triple{500, 1000, 1000}),
	}
}

// derajat Response Time (0-1000ms)
func GetRespLevel(val float64) map[string]float64 {
	return map[string]float64{
		"Cepat":  Fuzzify(val, Triple{0, 0, 500}),
		"Normal": Fuzzify(val, Triple{0, 500, 1000}),
		"Lambat": Fuzzify(val, Triple{500, 1000, 1000}),
	}
}
