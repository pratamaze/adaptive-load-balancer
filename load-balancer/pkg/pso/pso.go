package pso

import (
	"math"
	"math/rand"
	"time"
)

// Parameter statis yang di-hardcode berdasarkan proposal TA
const (
	W            = 0.5 // Inertia weight
	C1           = 1.5 // Cognitive parameter
	C2           = 1.5 // Social parameter
	Dimensions   = 27  // Jumlah parameter Fuzzy (x[27])
	NumParticles = 10  // Jumlah partikel P = {p1, p2, ..., p10}
	Iterations   = 300 // n_iter
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
		s.Particles[i] = p
	}

	return s
}

// Optimize menjalankan iterasi PSO dan mengembalikan gbest
func (s *Swarm) Optimize() []float64 {
	// 1: For t in n_iter:
	for t := 0; t < Iterations; t++ {

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
				p.V[d] = (W * p.V[d]) +
					(C1 * r1 * (p.PBest[d] - p.X[d])) +
					(C2 * r2 * (s.GBest[d] - p.X[d]))

				// 10: xi(t+1) = xi(t) + vi(t+1)
				p.X[d] = p.X[d] + p.V[d]

				// Optional Clamping: Mencegah nilai parameter FL menjadi negatif absolut (sesuaikan jika batas minimal FL bukan 0)
				if p.X[d] < 0.0 {
					p.X[d] = 0.0
				}
			}
		}
	}

	// 13: return gbest
	return s.GBest
}
