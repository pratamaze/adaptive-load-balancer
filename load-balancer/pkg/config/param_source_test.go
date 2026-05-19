package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"load-balancer/pkg/algorithm/fuzzy"
)

func TestInitializeFuzzyEnginesBaseModeUsesStrictBaseAndOutputMF(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir tempDir error: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})

	writeFileForTest(t, filepath.Join("configs", "base_fuzzy_params.json"), `[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26]`)
	writeFileForTest(t, filepath.Join("configs", "fuzzy_output_mf.json"), `{"rendah":[0,0,50],"sedang":[25,50,75],"tinggi":[50,100,100]}`)

	bootstrap, err := initializeFuzzyEngines("fuzzy_base", "base")
	if err != nil {
		t.Fatalf("initializeFuzzyEngines error: %v", err)
	}
	if bootstrap.BaseEngine == nil {
		t.Fatal("BaseEngine nil")
	}
	if bootstrap.MOPSOEngine == nil {
		t.Fatal("MOPSOEngine nil")
	}
	if bootstrap.ParamProfile != "BASE" {
		t.Fatalf("ParamProfile = %s, want BASE", bootstrap.ParamProfile)
	}
	if got := bootstrap.BaseEngine.GetParams(); len(got) != fuzzy.ParamCount {
		t.Fatalf("len(BaseEngine params) = %d, want %d", len(got), fuzzy.ParamCount)
	}
}

func TestInitializeFuzzyEnginesOptimizedModeRequiresOptimizedFile(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir tempDir error: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})

	writeFileForTest(t, filepath.Join("configs", "base_fuzzy_params.json"), `[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26]`)
	writeFileForTest(t, filepath.Join("configs", "fuzzy_output_mf.json"), `{"rendah":[0,0,50],"sedang":[25,50,75],"tinggi":[50,100,100]}`)

	_, err = initializeFuzzyEngines("fuzzy_base", "optimized")
	if err == nil {
		t.Fatal("initializeFuzzyEngines should fail when optimized file is missing in optimized mode")
	}
	if !strings.Contains(err.Error(), "optimized params") {
		t.Fatalf("initializeFuzzyEngines error = %v, want optimized params context", err)
	}
}

func TestInitializeFuzzyEnginesRejectsInvalidOutputMF(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir tempDir error: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})

	writeFileForTest(t, filepath.Join("configs", "base_fuzzy_params.json"), `[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26]`)
	writeFileForTest(t, filepath.Join("configs", "optimized_fuzzy_params.json"), `[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26]`)
	writeFileForTest(t, filepath.Join("configs", "fuzzy_output_mf.json"), `{"rendah":[0,0],"sedang":[25,50,75],"tinggi":[50,100,100]}`)

	_, err = initializeFuzzyEngines("fuzzy_mopso", "optimized")
	if err == nil {
		t.Fatal("initializeFuzzyEngines should fail for invalid output MF config")
	}
	if !strings.Contains(err.Error(), "output MF") {
		t.Fatalf("initializeFuzzyEngines error = %v, want output MF context", err)
	}
}

func writeFileForTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
}
