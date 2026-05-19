package fuzzy

import (
	"encoding/json"
	"fmt"
)

const ParamCount = 27

type outputMFDocument struct {
	Rendah []float64 `json:"rendah"`
	Sedang []float64 `json:"sedang"`
	Tinggi []float64 `json:"tinggi"`
}

func ParseOutputMFConfig(data []byte) ([3]Triple, error) {
	var doc outputMFDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return [3]Triple{}, fmt.Errorf("gagal decode output MF JSON: %w", err)
	}
	return BuildOutputMFSet(doc.Rendah, doc.Sedang, doc.Tinggi)
}

func BuildOutputMFSet(rendah, sedang, tinggi []float64) ([3]Triple, error) {
	low, err := tripleFromSlice("rendah", rendah)
	if err != nil {
		return [3]Triple{}, err
	}
	mid, err := tripleFromSlice("sedang", sedang)
	if err != nil {
		return [3]Triple{}, err
	}
	high, err := tripleFromSlice("tinggi", tinggi)
	if err != nil {
		return [3]Triple{}, err
	}
	return [3]Triple{low, mid, high}, nil
}

func tripleFromSlice(label string, values []float64) (Triple, error) {
	if len(values) != 3 {
		return Triple{}, fmt.Errorf("output MF %s harus berisi tepat 3 angka, dapat %d", label, len(values))
	}
	return Triple{A: values[0], B: values[1], C: values[2]}, nil
}
