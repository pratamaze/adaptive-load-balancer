package fuzzy

import (
	"fmt"
	"math"
	"testing"
)

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

	for _, tc := range testCases {
		// Menggunakan fungsi CalculateMamdani sesuai algoritma skripsi
		score := CalculateMamdani(tc.metrics, rules)

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
