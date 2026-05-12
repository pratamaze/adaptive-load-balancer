package types

import "math"

const defaultCPUCapacity = 100.0

// CalculateNormalizedCPU mengubah raw CPU (%) menjadi normalized CPU (%)
// berdasarkan kapasitas node. Hasil dibatasi pada rentang 0..100.
func CalculateNormalizedCPU(raw float64, capacity float64) float64 {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw <= 0 {
		return 0
	}

	capacityValue := capacity
	if math.IsNaN(capacityValue) || math.IsInf(capacityValue, 0) || capacityValue <= 0 {
		capacityValue = defaultCPUCapacity
	}

	normalized := (raw / capacityValue) * 100.0
	if normalized <= 0 {
		return 0
	}
	if normalized > 100 {
		return 100
	}
	return normalized
}
