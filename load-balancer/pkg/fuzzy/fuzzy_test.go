package fuzzy

import (
	"fmt"
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
