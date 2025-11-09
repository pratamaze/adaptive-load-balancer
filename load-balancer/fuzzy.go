package main

import (
	"log"
	"math"
)

// TriangularMF (Triangular Membership Function)
// Sesuai pseudocode (a,b,c)
type TriangularMF struct {
	A, B, C float64
}

// Fuzzify menghitung nilai keanggotaan (μ) untuk input crisp
func (mf *TriangularMF) Fuzzify(val float64) float64 {
	if val <= mf.A || val >= mf.C {
		return 0.0 // Di luar jangkauan
	}
	if val > mf.A && val <= mf.B {
		return (val - mf.A) / (mf.B - mf.A) // Sisi kiri (naik)
	}
	if val > mf.B && val < mf.C {
		return (mf.C - val) / (mf.C - mf.B) // Sisi kanan (turun)
	}
	return 0.0
}

// Centroid menghitung titik tengah TMF (Pseudocode baris 21)
func (mf *TriangularMF) Centroid() float64 {
	return (mf.A + mf.B + mf.C) / 3.0
}

// Area menghitung luas TMF (Pseudocode baris 20)
// Luas = 0.5 * (c - a) * (tinggi_puncak=1.0)
func (mf *TriangularMF) Area() float64 {
	return (mf.C - mf.A) / 2.0
}

// FuzzyRule merepresentasikan satu aturan IF-THEN
// (IF CPU is 'Label' AND Queue is 'Label' AND Resp is 'Label' THEN Output is 'Label')
type FuzzyRule struct {
	CPU    string // Label (cth: "rendah", "sedang", "tinggi")
	Queue  string // Label (cth: "rendah", "sedang", "tinggi")
	Resp   string // Label (cth: "rendah", "sedang", "tinggi")
	Output string // Label (cth: "rendah", "sedang", "tinggi")
}

// FuzzySystem menampung semua konfigurasi
type FuzzySystem struct {
	MFIn  map[string]map[string]TriangularMF // MF_in (cpu, queue, resp)
	MFOut map[string]TriangularMF            // MF_out (output score)
	Rules []FuzzyRule                        // Rules
}

// --- FUNGSI PROSES FUZZY (HELPERS) ---

// fuzzifyInputs (Pseudocode baris 2-4)
// Mengubah input crisp (CPU, Queue, Resp) menjadi derajat keanggotaan (μ)
func (fs *FuzzySystem) fuzzifyInputs(cpu, queue, resp float64) (
	map[string]float64, map[string]float64, map[string]float64) {

	muCPU := make(map[string]float64)
	muQueue := make(map[string]float64)
	muResp := make(map[string]float64)

	for label, mf := range fs.MFIn["cpu"] {
		muCPU[label] = mf.Fuzzify(cpu)
	}
	for label, mf := range fs.MFIn["queue"] {
		muQueue[label] = mf.Fuzzify(queue)
	}
	for label, mf := range fs.MFIn["resp"] {
		muResp[label] = mf.Fuzzify(resp)
	}
	return muCPU, muQueue, muResp
}

// applyInference (Pseudocode baris 6-13)
// Menerapkan aturan (AND = min) untuk mendapatkan firing strength (α)
func (fs *FuzzySystem) applyInference(muCPU, muQueue, muResp map[string]float64) []struct {
	Alpha float64
	Label string
} {
	alphaRules := []struct {
		Alpha float64
		Label string
	}{}

	for _, rule := range fs.Rules {
		muC, _ := muCPU[rule.CPU]
		muQ, _ := muQueue[rule.Queue]
		muR, _ := muResp[rule.Resp]

		// Terapkan operator AND (min) (Pseudocode baris 7)
		alpha := math.Min(muC, math.Min(muQ, muR))

		// (Pseudocode baris 12)
		if alpha > 0 {
			alphaRules = append(alphaRules, struct {
				Alpha float64
				Label string
			}{Alpha: alpha, Label: rule.Output})
		}
	}
	return alphaRules
}

// aggregateOutputs (Pseudocode baris 15-18)
// Agregasi hasil aturan (OR = max)
func (fs *FuzzySystem) aggregateOutputs(alphaRules []struct {
	Alpha float64
	Label string
}) map[string]float64 {

	alphaOut := make(map[string]float64)
	for label := range fs.MFOut {
		alphaOut[label] = 0.0
	}

	for _, ar := range alphaRules {
		if ar.Alpha > alphaOut[ar.Label] {
			alphaOut[ar.Label] = ar.Alpha
		}
	}
	return alphaOut
}

// defuzzify (Pseudocode baris 19-29)
// Defuzzifikasi menggunakan Center of Area / Center of Sums
func (fs *FuzzySystem) defuzzify(alphaOut map[string]float64) float64 {
	mTotal := 0.0 // M_total
	aTotal := 0.0 // A_total

	for label, mf := range fs.MFOut {
		alpha := alphaOut[label] // (Pseudocode baris 21)

		if alpha > 0 {
			area := alpha * mf.Area() // A = α * (Full_Area)
			centroid := mf.Centroid() // (a+b+c)/3
			moment := area * centroid // M = A * Centroid

			aTotal += area   // A_total
			mTotal += moment // M_total
		}
	}

	// (Pseudocode baris 29)
	if aTotal == 0 {
		log.Println("[FUZZY] Peringatan: aTotal=0, tidak ada aturan yang terpicu.")
		return 0.0 // atau nilai default yang aman
	}

	score := mTotal / aTotal
	return score
}

// --- FUNGSI UTAMA (PUBLIC) ---

// CalculateScore menjalankan sistem fuzzy lengkap untuk satu set input
// Ini adalah implementasi dari pseudocode baris 1-29
func (fs *FuzzySystem) CalculateScore(cpu, queue, resp float64) float64 {
	// 2. Fuzzify (Pseudocode baris 2-4)
	muCPU, muQueue, muResp := fs.fuzzifyInputs(cpu, queue, resp)

	// 3. Inference (Pseudocode baris 6-13)
	alphaRules := fs.applyInference(muCPU, muQueue, muResp)

	// 4. Aggregation (Pseudocode baris 15-18)
	alphaOut := fs.aggregateOutputs(alphaRules)

	// 5. Defuzzify (Pseudocode baris 19-29)
	score := fs.defuzzify(alphaOut)

	return score
}

// --- KONFIGURASI FUZZY ---
func newFuzzySystem() *FuzzySystem {

	// MF_in (Input)
	mfIn := make(map[string]map[string]TriangularMF)

	// MF_in.cpu (0-100%)
	mfIn["cpu"] = map[string]TriangularMF{
		"rendah": {A: 0, B: 0, C: 40},
		"sedang": {A: 30, B: 50, C: 70},
		"tinggi": {A: 60, B: 100, C: 100},
	}

	// MF_in.queue (Load Average, misal 0-10)
	mfIn["queue"] = map[string]TriangularMF{
		"rendah": {A: 0, B: 0, C: 2.0},
		"sedang": {A: 1.5, B: 3.0, C: 4.5},
		"tinggi": {A: 4.0, B: 7.0, C: 10.0},
	}

	// MF_in.resp (Response Time ms, misal 0-500ms)
	mfIn["resp"] = map[string]TriangularMF{
		"rendah": {A: 0, B: 0, C: 50},
		"sedang": {A: 40, B: 100, C: 150},
		"tinggi": {A: 120, B: 300, C: 500},
	}

	// MF_out (Output Score, 0-100, rendah=baik, tinggi=buruk)
	mfOut := map[string]TriangularMF{
		"rendah": {A: 0, B: 15, C: 30},
		"sedang": {A: 25, B: 50, C: 75},
		"tinggi": {A: 70, B: 85, C: 100},
	}

	// Rules (Basis Aturan)
	rules := []FuzzyRule{
		{CPU: "rendah", Queue: "rendah", Resp: "rendah", Output: "rendah"},
		{CPU: "tinggi", Queue: "tinggi", Resp: "tinggi", Output: "tinggi"},
		{CPU: "sedang", Queue: "sedang", Resp: "sedang", Output: "sedang"},
		{CPU: "tinggi", Queue: "rendah", Resp: "rendah", Output: "sedang"},
		{CPU: "rendah", Queue: "tinggi", Resp: "rendah", Output: "sedang"},
		{CPU: "rendah", Queue: "rendah", Resp: "tinggi", Output: "tinggi"},
		{CPU: "sedang", Queue: "rendah", Resp: "rendah", Output: "rendah"},
		{CPU: "sedang", Queue: "tinggi", Resp: "tinggi", Output: "tinggi"},
	}

	return &FuzzySystem{
		MFIn:  mfIn,
		MFOut: mfOut,
		Rules: rules,
	}
}
