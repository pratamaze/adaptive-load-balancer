package fuzzy

// kurva segitiga (A, B, C)
type Triple struct {
	A, B, C float64
}

// menampung parameter input
type NodeMetrics struct {
	CPU         float64
	QueueLength float64
	RespTime    float64
}

// relasi IF (labels) THEN (output_label)
type Rule struct {
	CPULabel    string
	QueueLabel  string
	RespLabel   string
	OutputLabel string
}
