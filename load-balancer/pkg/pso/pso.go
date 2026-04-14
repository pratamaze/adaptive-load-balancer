package pso

import (
	"math"
	"math/rand"
	"time"
)

// Parameter statis yang di-hardcode berdasarkan proposal TA
const (
	InertiaMaxW  = 0.90 // eksplorasi awal
	InertiaMinW  = 0.38 // eksploitasi akhir
	C1           = 1.20 // Cognitive parameter
	C2           = 2.00 // Social parameter
	Dimensions   = 27   // Jumlah parameter Fuzzy (x[27])
	NumParticles = 18   // Jumlah partikel P = {p1, p2, ..., p18}
	Iterations   = 520  // n_iter
)

// Particle merepresentasikan p dalam P
type Particle struct {
	X            []float64 // Posisi saat ini
	V            []float64 // Kecepatan
	PBest        []float64 // pbest
	PBestFitness float64   // Nilai fitness dari pbest
}

// Swarm merepresentasikan lingkungan optimasi
type Swarm struct {
	Particles    []*Particle
	GBest        []float64 // gbest (set parameter FL terbaik)
	GBestFitness float64
	FitnessFunc  func(x []float64) float64 // fitness(p)
}

// NewSwarm menginisialisasi populasi partikel
func NewSwarm(initialFLParams []float64, fitnessFunc func([]float64) float64) *Swarm {
	rand.Seed(time.Now().UnixNano())

	s := &Swarm{
		Particles:    make([]*Particle, NumParticles),
		GBest:        make([]float64, Dimensions),
		GBestFitness: -math.MaxFloat64,
		FitnessFunc:  fitnessFunc,
	}

	for i := 0; i < NumParticles; i++ {
		p := &Particle{
			X:            make([]float64, Dimensions),
			V:            make([]float64, Dimensions),
			PBest:        make([]float64, Dimensions),
			PBestFitness: -math.MaxFloat64,
		}

		for d := 0; d < Dimensions; d++ {
			// Inisialisasi posisi berdasarkan base x[27] dengan sedikit variasi acak
			p.X[d] = initialFLParams[d] + (rand.Float64()*20 - 10)

			// Inisialisasi kecepatan v[27] = random(0, 100)
			p.V[d] = rand.Float64() * 100

			p.PBest[d] = p.X[d]
		}
		repairParams(p.X)
		copy(p.PBest, p.X)
		s.Particles[i] = p
	}

	return s
}

// Optimize menjalankan iterasi PSO dan mengembalikan gbest
func (s *Swarm) Optimize() []float64 {
	// 1: For t in n_iter:
	for t := 0; t < Iterations; t++ {
		w := InertiaMaxW - (InertiaMaxW-InertiaMinW)*(float64(t)/float64(Iterations-1))

		// 2: For each particle p in P:
		for _, p := range s.Particles {
			// 3: fp = fitness(p)
			fp := s.FitnessFunc(p.X)

			// 4: if fp is better than pbest
			if fp > p.PBestFitness {
				// 5: pbest = p
				p.PBestFitness = fp
				copy(p.PBest, p.X)
			}

			// 7: gbest = best p in P (Pembaruan global best langsung dikaitkan saat iterasi partikel)
			if p.PBestFitness > s.GBestFitness {
				s.GBestFitness = p.PBestFitness
				copy(s.GBest, p.PBest)
			}
		}

		// 8: For each particle i:
		for _, p := range s.Particles {
			for d := 0; d < Dimensions; d++ {
				r1 := rand.Float64()
				r2 := rand.Float64()

				// 9: vi(t+1) = w*vi(t) + c1*r1*(pbesti - xi(t)) + c2*r2*(gbest - xi(t))
				p.V[d] = (w * p.V[d]) +
					(C1 * r1 * (p.PBest[d] - p.X[d])) +
					(C2 * r2 * (s.GBest[d] - p.X[d]))

				// 10: xi(t+1) = xi(t) + vi(t+1)
				p.X[d] = p.X[d] + p.V[d]
			}
			repairParams(p.X)
		}
	}

	// 13: return gbest
	return s.GBest
}

func upperBound(d int) float64 {
	switch {
	case d <= 8:
		return 100
	case d <= 17:
		return 2000
	default:
		return 2000
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func repairParams(params []float64) {
	const eps = 1e-6
	for i := 0; i+2 < len(params); i += 3 {
		lo := 0.0
		hi := upperBound(i)
		a := clamp(params[i], lo, hi)
		b := clamp(params[i+1], lo, hi)
		c := clamp(params[i+2], lo, hi)

		if a > b {
			a, b = b, a
		}
		if b > c {
			b, c = c, b
		}
		if a > b {
			a, b = b, a
		}
		if b < a+eps {
			b = a + eps
		}
		if c < b+eps {
			c = b + eps
		}
		if c > hi {
			c = hi
			if b > c-eps {
				b = c - eps
			}
			if b < a+eps {
				a = b - eps
			}
		}
		if a < lo {
			a = lo
		}
		if b < a+eps {
			b = a + eps
		}
		if c < b+eps {
			c = b + eps
		}
		if c > hi {
			c = hi
		}

		params[i] = a
		params[i+1] = b
		params[i+2] = c
	}
}
