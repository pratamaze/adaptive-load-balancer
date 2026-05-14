package config

import "testing"

func TestInitBackendNodes_DefaultCPUCapacities(t *testing.T) {
	t.Setenv("NODE1_CPU_LIMIT_PERCENT", "")
	t.Setenv("NODE2_CPU_LIMIT_PERCENT", "")
	t.Setenv("API_NODE1_CPU_LIMIT_PERCENT", "")
	t.Setenv("API_NODE2_CPU_LIMIT_PERCENT", "")
	t.Setenv("CPU_LIMIT_PERCENT", "")

	nodes, err := initBackendNodes([]string{"http://api-node1:8080", "http://api-node2:8080"})
	if err != nil {
		t.Fatalf("initBackendNodes error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("len(nodes) = %d, want 2", len(nodes))
	}
	if nodes[0].CPUCap != 100 {
		t.Fatalf("node1 CPUCap = %v, want 100", nodes[0].CPUCap)
	}
	if nodes[1].CPUCap != 50 {
		t.Fatalf("node2 CPUCap = %v, want 50", nodes[1].CPUCap)
	}
}

func TestInitBackendNodes_CPUCapacityEnvOverride(t *testing.T) {
	t.Setenv("NODE1_CPU_LIMIT_PERCENT", "")
	t.Setenv("NODE2_CPU_LIMIT_PERCENT", "60")
	t.Setenv("API_NODE1_CPU_LIMIT_PERCENT", "")
	t.Setenv("API_NODE2_CPU_LIMIT_PERCENT", "")
	t.Setenv("CPU_LIMIT_PERCENT", "90")

	nodes, err := initBackendNodes([]string{"http://api-node1:8080", "http://api-node2:8080"})
	if err != nil {
		t.Fatalf("initBackendNodes error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("len(nodes) = %d, want 2", len(nodes))
	}
	// fallback CPU_LIMIT_PERCENT untuk node1
	if nodes[0].CPUCap != 90 {
		t.Fatalf("node1 CPUCap = %v, want 90", nodes[0].CPUCap)
	}
	// override spesifik NODE2_CPU_LIMIT_PERCENT
	if nodes[1].CPUCap != 60 {
		t.Fatalf("node2 CPUCap = %v, want 60", nodes[1].CPUCap)
	}
}
