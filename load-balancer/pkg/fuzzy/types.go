package fuzzy

// Triple merepresentasikan kurva segitiga (A, B, C)
type Triple struct {
	A, B, C float64
}

// NodeMetrics menampung parameter input sesuai skripsi
type NodeMetrics struct {
	CPU         float64
	QueueLength float64
	RespTime    float64
}

// Rule mendefinisikan relasi IF (labels) THEN (output_label)
type Rule struct {
	CPULabel    string
	QueueLabel  string
	RespLabel   string
	OutputLabel string
}
