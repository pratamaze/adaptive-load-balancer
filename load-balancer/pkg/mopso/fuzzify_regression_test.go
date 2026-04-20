package mopso

import "testing"

func TestFuzzify_LeftShoulder_NearSanitized(t *testing.T) {
	got := fuzzify(0, 0, 0.000001, 500)
	if got < 0.99 {
		t.Fatalf("expected near-left shoulder membership close to 1, got %.8f", got)
	}
}

func TestFuzzify_RightShoulder_NearSanitized(t *testing.T) {
	got := fuzzify(1000, 500, 999.999999, 1000)
	if got < 0.99 {
		t.Fatalf("expected near-right shoulder membership close to 1, got %.8f", got)
	}
}

