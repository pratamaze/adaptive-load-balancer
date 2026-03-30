package roundrobin

import (
	"sync/atomic"
)

// Balancer menyimpan state Round Robin
type Balancer struct {
	counter uint64
}

// New membuat instance baru
func New() *Balancer {
	return &Balancer{
		counter: 0,
	}
}

// NextIndex murni melakukan perhitungan matematis secara Thread-Safe
func (b *Balancer) NextIndex(totalNodes int) int {
	if totalNodes <= 0 {
		return -1
	}

	// atomic.AddUint64 mengembalikan nilai SETELAH ditambah
	current := atomic.AddUint64(&b.counter, 1)

	// Modulo untuk rotasi indeks
	return int((current - 1) % uint64(totalNodes))
}
