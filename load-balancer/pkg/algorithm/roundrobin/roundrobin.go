package roundrobin

import (
	"sync/atomic"
)

// state Round Robin
type Balancer struct {
	counter uint64
}

// instance baru
func New() *Balancer {
	return &Balancer{
		counter: 0,
	}
}

// NextIndex perhitungan secara Thread-Safe
func (b *Balancer) NextIndex(totalNodes int) int {
	if totalNodes <= 0 {
		return -1
	}

	// atomic.AddUint64 mengembalikan nilai setelah ditambah
	current := atomic.AddUint64(&b.counter, 1)

	// Modulo untuk rotasi indeks
	return int((current - 1) % uint64(totalNodes))
}
