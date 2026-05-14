package types

import "testing"

func TestCalculateNormalizedCPU(t *testing.T) {
	tests := []struct {
		name     string
		raw      float64
		capacity float64
		want     float64
	}{
		{name: "normal_capacity", raw: 40, capacity: 100, want: 40},
		{name: "limited_capacity", raw: 40, capacity: 50, want: 80},
		{name: "clamped_to_100", raw: 70, capacity: 50, want: 100},
		{name: "zero_raw", raw: 0, capacity: 50, want: 0},
		{name: "invalid_capacity_fallback", raw: 30, capacity: 0, want: 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateNormalizedCPU(tt.raw, tt.capacity)
			if got != tt.want {
				t.Fatalf("CalculateNormalizedCPU(%v, %v) = %v, want %v", tt.raw, tt.capacity, got, tt.want)
			}
		})
	}
}
