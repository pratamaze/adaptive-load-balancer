package fuzzy

import (
	"fmt"
	"math"
	"testing"
)

func mustOutputMF(t *testing.T) [3]Triple {
	t.Helper()
	outputMF, err := BuildOutputMFSet(
		[]float64{0, 0, 50},
		[]float64{25, 50, 75},
		[]float64{50, 100, 100},
	)
	if err != nil {
		t.Fatalf("BuildOutputMFSet error: %v", err)
	}
	return outputMF
}

func TestFuzzyLogic(t *testing.T) {
	// 1. Definisikan Rule Base untuk pengujian (Minimal 3 Aturan Dasar)
	rules := []Rule{
		{CPULabel: "Rendah", QueueLabel: "Rendah", RespLabel: "Cepat", OutputLabel: "Tinggi"},
		{CPULabel: "Sedang", QueueLabel: "Sedang", RespLabel: "Normal", OutputLabel: "Sedang"},
		{CPULabel: "Tinggi", QueueLabel: "Tinggi", RespLabel: "Lambat", OutputLabel: "Rendah"},
	}

	// 2. Simulasi skenario node berdasarkan parameter skripsi Anda
	testCases := []struct {
		name    string
		metrics NodeMetrics
	}{
		{
			name:    "Node_Performa_Tinggi",
			metrics: NodeMetrics{CPU: 15.0, QueueLength: 10.0, RespTime: 50.0},
		},
		{
			name:    "Node_Beban_Menengah",
			metrics: NodeMetrics{CPU: 50.0, QueueLength: 150.0, RespTime: 350.0},
		},
		{
			name:    "Node_Kritis_Overload",
			metrics: NodeMetrics{CPU: 95.0, QueueLength: 800.0, RespTime: 900.0},
		},
	}

	fmt.Println("\n==========================================")
	fmt.Println("   HASIL SIMULASI MAMDANI (SKRIPSI)   ")
	fmt.Println("==========================================")

	// Gunakan jalur yang sama dengan runtime: engine instance + parameter eksplisit.
	defaultParams := []float64{
		0, 40, 75, 60, 80, 95, 85, 95, 100,
		0, 50, 150, 100, 250, 400, 300, 500, 1000,
		0, 150, 300, 200, 500, 800, 600, 850, 1000,
	}
	engine := NewEngine(defaultParams, mustOutputMF(t))

	for _, tc := range testCases {
		score := engine.CalculateMamdani(tc.metrics, rules)

		fmt.Printf("[%s]\n", tc.name)
		fmt.Printf("  -> Input  : CPU: %.1f%%, Queue: %.0f, Resp: %.1fms\n",
			tc.metrics.CPU, tc.metrics.QueueLength, tc.metrics.RespTime)
		fmt.Printf("  -> Output : Skor Kelayakan: %.4f\n", score)
		fmt.Println("------------------------------------------")
	}
}

func TestFuzzifyShoulderMembership(t *testing.T) {
	tests := []struct {
		name string
		val  float64
		mf   Triple
		want float64
	}{
		{
			name: "left_shoulder_at_zero_should_be_one",
			val:  0,
			mf:   Triple{0, 0, 50},
			want: 1.0,
		},
		{
			name: "left_shoulder_mid",
			val:  25,
			mf:   Triple{0, 0, 50},
			want: 0.5,
		},
		{
			name: "right_shoulder_high_should_be_one",
			val:  100,
			mf:   Triple{50, 100, 100},
			want: 1.0,
		},
		{
			name: "right_shoulder_ramp",
			val:  75,
			mf:   Triple{50, 100, 100},
			want: 0.5,
		},
	}

	const eps = 1e-9
	for _, tt := range tests {
		got := Fuzzify(tt.val, tt.mf)
		if math.Abs(got-tt.want) > eps {
			t.Fatalf("%s: got %.10f want %.10f", tt.name, got, tt.want)
		}
	}
}

func TestDefuzzifyMamdaniDiscreteCentroid(t *testing.T) {
	outputMF := mustOutputMF(t)
	tests := []struct {
		name  string
		alpha [3]float64
		want  float64
	}{
		{
			name:  "low_shoulder_matches_discrete_centroid",
			alpha: [3]float64{1, 0, 0},
			want:  416.5 / 25.5,
		},
		{
			name:  "medium_triangle_stays_centered",
			alpha: [3]float64{0, 1, 0},
			want:  50.0,
		},
		{
			name:  "high_shoulder_matches_discrete_centroid",
			alpha: [3]float64{0, 0, 1},
			want:  83.66666666666667,
		},
		{
			name:  "no_active_rule_returns_zero",
			alpha: [3]float64{},
			want:  0.0,
		},
	}

	const eps = 1e-9
	for _, tt := range tests {
		got := DefuzzifyMamdani(tt.alpha, outputMF)
		if math.Abs(got-tt.want) > eps {
			t.Fatalf("%s: got %.12f want %.12f", tt.name, got, tt.want)
		}
	}
}

func TestParseOutputMFConfig(t *testing.T) {
	data := []byte(`{"rendah":[0,0,50],"sedang":[25,50,75],"tinggi":[50,100,100]}`)
	got, err := ParseOutputMFConfig(data)
	if err != nil {
		t.Fatalf("ParseOutputMFConfig error: %v", err)
	}
	want := mustOutputMF(t)
	if got != want {
		t.Fatalf("ParseOutputMFConfig = %+v, want %+v", got, want)
	}
}

func TestParseOutputMFConfigRejectsInvalidTriples(t *testing.T) {
	data := []byte(`{"rendah":[0,0],"sedang":[25,50,75],"tinggi":[50,100,100]}`)
	if _, err := ParseOutputMFConfig(data); err == nil {
		t.Fatal("ParseOutputMFConfig should fail for invalid rendah triple length")
	}
}
