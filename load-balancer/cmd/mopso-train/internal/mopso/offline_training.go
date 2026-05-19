package mopso

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"time"

	"load-balancer/pkg/algorithm/fuzzy"
)

// OfflineSample adalah 1 jendela data hasil pengujian fuzzy biasa.
type OfflineSample struct {
	Timestamp string
	OSIdleCPU float64
	Node1     NodeState
	Node2     NodeState
}

// OfflineConfig mengatur ukuran swarm untuk training offline.
type OfflineConfig struct {
	Particles     int     `json:"particles"`
	Iterations    int     `json:"iterations"`
	InitialSpread float64 `json:"initial_spread"`
	Seed          int64   `json:"seed"`
}

// OfflineObjective memuat objective yang diminta: DI dan BCU.
type OfflineObjective struct {
	DI  float64 `json:"di"`
	BCU float64 `json:"bcu"`
}

// OfflineSolution menyimpan kandidat parameter dan objective hasil replay dataset.
type OfflineSolution struct {
	Params    []float64        `json:"params"`
	Objective OfflineObjective `json:"objective"`
}

// OfflineResult adalah output training MOPSO berbasis dataset.
type OfflineResult struct {
	GeneratedAt  string            `json:"generated_at"`
	SampleCount  int               `json:"sample_count"`
	UsedSamples  int               `json:"used_samples"`
	Config       OfflineConfig     `json:"config"`
	Archive      []OfflineSolution `json:"archive"`
	BestBalanced OfflineSolution   `json:"best_balanced"`
	BestDI       OfflineSolution   `json:"best_di"`
	BestBCU      OfflineSolution   `json:"best_bcu"`
}

// EvaluateOfflineObjective menghitung DI/BCU berdasarkan rumus evaluasi skripsi
// (berbasis rata-rata CPU node sepanjang dataset).
func EvaluateOfflineObjective(params []float64, outputMF [3]fuzzy.Triple, samples []OfflineSample) (OfflineObjective, error) {
	if len(params) != Dimensions {
		return OfflineObjective{}, errors.New("parameter harus berukuran 27")
	}
	if len(samples) == 0 {
		return OfflineObjective{}, errors.New("dataset kosong")
	}
	di, bcu := evaluateDatasetDIAndBCU(params, outputMF, samples)
	return OfflineObjective{DI: di, BCU: bcu}, nil
}

func (c OfflineConfig) normalized() OfflineConfig {
	out := c
	if out.Particles <= 0 {
		out.Particles = NumParticles
	}
	if out.Iterations <= 0 {
		out.Iterations = Iterations
	}
	if out.InitialSpread <= 0 {
		out.InitialSpread = 8
	}
	if out.Seed == 0 {
		out.Seed = time.Now().UnixNano()
	}
	return out
}

// OptimizeOffline menjalankan MOPSO menggunakan dataset CSV hasil replay fuzzy.
func OptimizeOffline(samples []OfflineSample, baseParams []float64, outputMF [3]fuzzy.Triple, cfg OfflineConfig) (OfflineResult, error) {
	result := OfflineResult{GeneratedAt: time.Now().Format(time.RFC3339), SampleCount: len(samples)}
	if len(baseParams) != Dimensions {
		return result, errors.New("base params harus berukuran 27")
	}
	if len(samples) == 0 {
		return result, errors.New("dataset kosong")
	}

	usedSamples := countUsableSamples(samples)
	result.UsedSamples = usedSamples
	if usedSamples == 0 {
		return result, errors.New("dataset tidak punya sample dengan total request > 0")
	}

	cfg = cfg.normalized()
	result.Config = cfg

	rng := rand.New(rand.NewSource(cfg.Seed))
	particles := make([]particle, cfg.Particles)
	archive := make([]Solution, 0, 64)

	for i := 0; i < cfg.Particles; i++ {
		x := make([]float64, Dimensions)
		v := make([]float64, Dimensions)
		pb := make([]float64, Dimensions)
		for d := 0; d < Dimensions; d++ {
			base := baseParams[d]
			delta := cfg.InitialSpread
			x[d] = clamp(base+(rng.Float64()*2*delta-delta), lowerBound(d), upperBound(d))
			v[d] = rng.Float64()*2 - 1
			pb[d] = x[d]
		}
		repairParams(x)
		copy(pb, x)
		particles[i] = particle{x: x, v: v, pbest: pb}
	}

	for t := 0; t < cfg.Iterations; t++ {
		w := inertiaMaxW - (inertiaMaxW-inertiaMinW)*(float64(t)/float64(maxInt(cfg.Iterations-1, 1)))

		for i := 0; i < cfg.Particles; i++ {
			p := &particles[i]
			di, bcu := evaluateDatasetDIAndBCU(p.x, outputMF, samples)
			obj := Objective{Imbalance: di, PeakLoad: 1.0 - bcu}

			if !p.hasBest || dominates(obj, p.pbestO) || (!dominates(p.pbestO, obj) && objectiveScore(obj) < objectiveScore(p.pbestO)) {
				p.pbestO = obj
				copy(p.pbest, p.x)
				p.hasBest = true
			}

			archive = addToArchive(archive, Solution{
				Params:    clone27(p.x),
				Objective: obj,
			})
		}

		if len(archive) == 0 {
			continue
		}

		for i := 0; i < cfg.Particles; i++ {
			p := &particles[i]
			leader := archive[rng.Intn(len(archive))]
			for d := 0; d < Dimensions; d++ {
				r1 := rng.Float64()
				r2 := rng.Float64()
				vel := w*p.v[d] + cognitiveC1*r1*(p.pbest[d]-p.x[d]) + socialC2*r2*(leader.Params[d]-p.x[d])
				next := p.x[d] + vel
				p.v[d] = vel
				p.x[d] = clamp(next, lowerBound(d), upperBound(d))
			}
			repairParams(p.x)
		}
	}

	converted := make([]OfflineSolution, 0, len(archive))
	for i := range archive {
		converted = append(converted, OfflineSolution{
			Params: clone27(archive[i].Params),
			Objective: OfflineObjective{
				DI:  archive[i].Objective.Imbalance,
				BCU: clamp(1.0-archive[i].Objective.PeakLoad, 0, 1),
			},
		})
	}

	sort.Slice(converted, func(i, j int) bool {
		return offlineBalancedScore(converted[i]) < offlineBalancedScore(converted[j])
	})
	result.Archive = converted
	if len(converted) == 0 {
		return result, errors.New("pareto archive kosong")
	}

	bestBalancedIdx, bestDIIdx, bestBCUIdx := 0, 0, 0
	for i := 1; i < len(converted); i++ {
		if converted[i].Objective.DI < converted[bestDIIdx].Objective.DI {
			bestDIIdx = i
		}
		if converted[i].Objective.BCU > converted[bestBCUIdx].Objective.BCU {
			bestBCUIdx = i
		}
		if offlineBalancedScore(converted[i]) < offlineBalancedScore(converted[bestBalancedIdx]) {
			bestBalancedIdx = i
		}
	}

	result.BestBalanced = converted[bestBalancedIdx]
	result.BestDI = converted[bestDIIdx]
	result.BestBCU = converted[bestBCUIdx]
	return result, nil
}

func evaluateDatasetDIAndBCU(params []float64, outputMF [3]fuzzy.Triple, samples []OfflineSample) (di, bcu float64) {
	var sumNormCPU1 float64
	var sumNormCPU2 float64
	var count int

	for i := range samples {
		s := samples[i]
		totalReq := s.Node1.Requests + s.Node2.Requests
		if totalReq <= 0 {
			continue
		}

		cap1 := s.Node1.CPUCapacity
		cap2 := s.Node2.CPUCapacity
		if cap1 <= 0 {
			cap1 = 100
		}
		if cap2 <= 0 {
			cap2 = 100
		}
		cpu1Fuzzy := toNormalizedCPU(s.Node1.CPUUsage, cap1)
		cpu2Fuzzy := toNormalizedCPU(s.Node2.CPUUsage, cap2)
		cpu1Raw := toRawCPU(s.Node1.CPUUsage, cap1)
		cpu2Raw := toRawCPU(s.Node2.CPUUsage, cap2)

		score1 := fuzzyScore(params, outputMF, cpu1Fuzzy, s.Node1.QueueLength, s.Node1.ResponseTime)
		score2 := fuzzyScore(params, outputMF, cpu2Fuzzy, s.Node2.QueueLength, s.Node2.ResponseTime)
		totalScore := score1 + score2

		share1 := 0.5
		if totalScore > 0 {
			share1 = score1 / totalScore
			if share1 < 0 {
				share1 = 0
			}
			if share1 > 1 {
				share1 = 1
			}
		}

		predReq1 := float64(totalReq) * share1
		predReq2 := float64(totalReq) - predReq1

		osIdle := s.OSIdleCPU
		if osIdle <= 0 {
			osIdle = 10
		}

		obsReq1 := maxInt64(s.Node1.Requests, 1)
		obsReq2 := maxInt64(s.Node2.Requests, 1)
		costPerReq1 := (cpu1Raw - osIdle) / float64(obsReq1)
		costPerReq2 := (cpu2Raw - osIdle) / float64(obsReq2)
		if costPerReq1 < 0 {
			costPerReq1 = 0
		}
		if costPerReq2 < 0 {
			costPerReq2 = 0
		}

		simCPU1 := osIdle + (costPerReq1 * predReq1)
		simCPU2 := osIdle + (costPerReq2 * predReq2)
		if simCPU1 < 0 {
			simCPU1 = 0
		}
		if simCPU2 < 0 {
			simCPU2 = 0
		}

		// Jaga hasil simulasi tetap realistis dalam rentang kapasitas node.
		if simCPU1 > cap1 {
			simCPU1 = cap1
		}
		if simCPU2 > cap2 {
			simCPU2 = cap2
		}

		// Konversi balik ke normalized CPU (0..100) sebelum evaluasi DI/BCU.
		normCPU1 := (simCPU1 / cap1) * 100.0
		normCPU2 := (simCPU2 / cap2) * 100.0
		if normCPU1 < 0 {
			normCPU1 = 0
		}
		if normCPU2 < 0 {
			normCPU2 = 0
		}
		if normCPU1 > 100 {
			normCPU1 = 100
		}
		if normCPU2 > 100 {
			normCPU2 = 100
		}

		sumNormCPU1 += normCPU1
		sumNormCPU2 += normCPU2
		count++
	}

	if count == 0 {
		return 1, 0
	}
	avgCPU1 := sumNormCPU1 / float64(count)
	avgCPU2 := sumNormCPU2 / float64(count)
	avgTotal := (avgCPU1 + avgCPU2) / 2.0

	if avgTotal > 0 {
		hi := math.Max(avgCPU1, avgCPU2)
		lo := math.Min(avgCPU1, avgCPU2)
		di = (hi - lo) / avgTotal
	} else {
		di = 0
	}

	if avgTotal < 1.0 {
		bcu = 1.0
	} else {
		dev1 := math.Abs(avgCPU1 - avgTotal)
		dev2 := math.Abs(avgCPU2 - avgTotal)
		bcu = 1.0 - ((dev1 + dev2) / (2.0 * avgTotal))
	}
	if bcu < 0 {
		bcu = 0
	}
	if bcu > 1 {
		bcu = 1
	}
	return di, bcu
}

func objectiveScore(obj Objective) float64 {
	return obj.Imbalance + obj.PeakLoad
}

func offlineBalancedScore(s OfflineSolution) float64 {
	return s.Objective.DI + (1 - s.Objective.BCU)
}

func countUsableSamples(samples []OfflineSample) int {
	count := 0
	for i := range samples {
		if samples[i].Node1.Requests+samples[i].Node2.Requests > 0 {
			count++
		}
	}
	return count
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
