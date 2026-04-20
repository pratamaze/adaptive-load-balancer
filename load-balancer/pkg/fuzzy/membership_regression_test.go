package fuzzy

import (
	"math"
	"testing"
)

func TestFuzzify_LeftShoulder(t *testing.T) {
	mf := Triple{0, 0, 500}
	if got := Fuzzify(0, mf); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("left shoulder at val=0 should be 1, got %.8f", got)
	}
}

func TestFuzzify_LeftShoulder_NearSanitized(t *testing.T) {
	// Mirip hasil sanitize: a=0, b=1e-6, c=500.
	mf := Triple{0, 0.000001, 500}
	if got := Fuzzify(0, mf); got < 0.99 {
		t.Fatalf("near-left shoulder at val=0 should stay close to 1, got %.8f", got)
	}
}

func TestFuzzify_RightShoulder_NearSanitized(t *testing.T) {
	// Mirip hasil sanitize: a=500, b=999.999999, c=1000.
	mf := Triple{500, 999.999999, 1000}
	if got := Fuzzify(1000, mf); got < 0.99 {
		t.Fatalf("near-right shoulder at val=1000 should stay close to 1, got %.8f", got)
	}
}

func TestCalculateMamdani_QueueZero_ShouldNotCollapseToZero(t *testing.T) {
	params := []float64{
		0, 0.000001, 50, 0, 50, 100, 50, 99.999999, 100,
		0, 0.000001, 500, 0, 500, 1000, 500, 999.999999, 1000,
		0, 0.000001, 500, 0, 500, 1000, 500, 999.999999, 1000,
	}
	engine := NewEngine(params)

	rules := []Rule{
		{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
		{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
		{CPULabel: "Tinggi", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},
	}
	score := engine.CalculateMamdani(NodeMetrics{
		CPU:         1.0,
		QueueLength: 0.0,
		RespTime:    20.0,
	}, rules)
	if score <= 0 {
		t.Fatalf("score should be > 0 for healthy low-load input, got %.8f", score)
	}
}

