package mopso

import (
	"math"
	"math/rand"
	"sort"
	"time"
)

const (
	Dimensions   = 27
	NumParticles = 20
	Iterations   = 1800
	maxArchive   = 128
	inertiaMaxW  = 0.92
	inertiaMinW  = 0.36
	cognitiveC1  = 1.15
	socialC2     = 2.05
)

// NodeState menyimpan data historis + metrik observasi saat replay.
type NodeState struct {
	CPUUsage     float64
	CPUCapacity  float64
	QueueLength  float64
	ResponseTime float64
	Requests     int64
}

// HistoricalSnapshot adalah data 5 detik terakhir untuk replay.
type HistoricalSnapshot struct {
	OSIdleCPU10 float64
	Node1       NodeState
	Node2       NodeState
}

// Objective adalah fitness multi-objective yang diminimalkan.
type Objective struct {
	Imbalance float64 `json:"f1_imbalance"`
	PeakLoad  float64 `json:"f2_peak_load"`
}

// Solution menyimpan parameter + hasil simulasi.
type Solution struct {
	Params        []float64 `json:"params"`
	Objective     Objective `json:"objective"`
	SimulatedCPU1 float64   `json:"simulated_cpu_node1"`
	SimulatedCPU2 float64   `json:"simulated_cpu_node2"`
}

// Compromise adalah solusi representatif untuk mode bisnis.
type Compromise struct {
	Mode     string   `json:"mode"`
	Solution Solution `json:"solution"`
}

// ParetoResult adalah output akhir optimasi.
type ParetoResult struct {
	Archive      []Solution   `json:"archive"`
	Compromises  []Compromise `json:"compromises"`
	TotalRequest int64        `json:"total_request"`
	CostPerReq1  float64      `json:"cost_per_req_node1"`
	CostPerReq2  float64      `json:"cost_per_req_node2"`
}

type particle struct {
	x       []float64
	v       []float64
	pbest   []float64
	pbestO  Objective
	hasBest bool
}

type evaluator struct {
	snap       HistoricalSnapshot
	totalReq   int64
	costPerReq [2]float64
	capacity   [2]float64
}

func newEvaluator(snap HistoricalSnapshot) evaluator {
	r1 := maxInt64(snap.Node1.Requests, 1)
	r2 := maxInt64(snap.Node2.Requests, 1)
	c1 := (snap.Node1.CPUUsage - snap.OSIdleCPU10) / float64(r1)
	c2 := (snap.Node2.CPUUsage - snap.OSIdleCPU10) / float64(r2)
	if c1 < 0 {
		c1 = 0
	}
	if c2 < 0 {
		c2 = 0
	}
	cap1 := snap.Node1.CPUCapacity
	if cap1 <= 0 {
		cap1 = 100
	}
	cap2 := snap.Node2.CPUCapacity
	if cap2 <= 0 {
		cap2 = 100
	}
	return evaluator{
		snap:       snap,
		totalReq:   snap.Node1.Requests + snap.Node2.Requests,
		costPerReq: [2]float64{c1, c2},
		capacity:   [2]float64{cap1, cap2},
	}
}

func (e evaluator) evaluate(params []float64) (Objective, float64, float64) {
	score1 := fuzzyScore(params, e.snap.Node1.CPUUsage, e.snap.Node1.QueueLength, e.snap.Node1.ResponseTime)
	score2 := fuzzyScore(params, e.snap.Node2.CPUUsage, e.snap.Node2.QueueLength, e.snap.Node2.ResponseTime)
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

	r1 := float64(e.totalReq) * share1
	r2 := float64(e.totalReq) - r1

	sim1 := e.snap.OSIdleCPU10 + e.costPerReq[0]*r1
	sim2 := e.snap.OSIdleCPU10 + e.costPerReq[1]*r2
	if sim1 < 0 {
		sim1 = 0
	}
	if sim2 < 0 {
		sim2 = 0
	}

	// Objective disejajarkan ke kapasitas node heterogen.
	util1 := sim1 / maxFloat(e.capacity[0], 1e-9)
	util2 := sim2 / maxFloat(e.capacity[1], 1e-9)
	sumCPU := util1 + util2
	if sumCPU < 1e-9 {
		sumCPU = 1e-9
	}
	di := math.Abs(util1-util2) / sumCPU
	hi := math.Max(util1, util2)
	lo := math.Min(util1, util2)
	bcu := lo / maxFloat(hi, 1e-9)
	diPenalty := (2.8 * di * di) + (1.4 * (1.0 - bcu) * (1.0 - bcu))
	latTail := math.Max(e.snap.Node1.ResponseTime, e.snap.Node2.ResponseTime) / 1000.0
	peakPenalty := hi + 0.30*latTail

	// f1 = penalti keseimbangan berbasis DI+BCU.
	// f2 = penalti puncak utilitas node + latensi tail.
	obj := Objective{
		Imbalance: diPenalty,
		PeakLoad:  peakPenalty,
	}
	return obj, sim1, sim2
}

// OptimizeReplay menjalankan F-MOPSO murni dari historical replay 5 detik terakhir.
func OptimizeReplay(baseParams []float64, snap HistoricalSnapshot) ParetoResult {
	eval := newEvaluator(snap)
	result := ParetoResult{
		TotalRequest: eval.totalReq,
		CostPerReq1:  eval.costPerReq[0],
		CostPerReq2:  eval.costPerReq[1],
	}
	if eval.totalReq == 0 {
		return result
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	particles := make([]particle, NumParticles)
	archive := make([]Solution, 0, 32)

	for i := 0; i < NumParticles; i++ {
		x := make([]float64, Dimensions)
		v := make([]float64, Dimensions)
		pb := make([]float64, Dimensions)
		for d := 0; d < Dimensions; d++ {
			base := baseParams[d]
			rangeDelta := 8.0
			x[d] = clamp(base+(rng.Float64()*2*rangeDelta-rangeDelta), lowerBound(d), upperBound(d))
			v[d] = rng.Float64()*2 - 1
			pb[d] = x[d]
		}
		repairParams(x)
		copy(pb, x)
		particles[i] = particle{x: x, v: v, pbest: pb}
	}

	for t := 0; t < Iterations; t++ {
		w := inertiaMaxW - (inertiaMaxW-inertiaMinW)*(float64(t)/float64(Iterations-1))
		for i := 0; i < NumParticles; i++ {
			p := &particles[i]
			obj, sim1, sim2 := eval.evaluate(p.x)

			if !p.hasBest || dominates(obj, p.pbestO) || (!dominates(p.pbestO, obj) && obj.PeakLoad < p.pbestO.PeakLoad) {
				p.pbestO = obj
				copy(p.pbest, p.x)
				p.hasBest = true
			}

			archive = addToArchive(archive, Solution{
				Params:        clone27(p.x),
				Objective:     obj,
				SimulatedCPU1: sim1,
				SimulatedCPU2: sim2,
			})
		}

		if len(archive) == 0 {
			continue
		}

		for i := 0; i < NumParticles; i++ {
			p := &particles[i]
			leader := archive[rng.Intn(len(archive))]
			for d := 0; d < Dimensions; d++ {
				r1 := rng.Float64()
				r2 := rng.Float64()
				v := w*p.v[d] + cognitiveC1*r1*(p.pbest[d]-p.x[d]) + socialC2*r2*(leader.Params[d]-p.x[d])
				x := p.x[d] + v
				p.v[d] = v
				p.x[d] = clamp(x, lowerBound(d), upperBound(d))
			}
			repairParams(p.x)
		}
	}

	result.Archive = archive
	result.Compromises = buildCompromises(archive)
	return result
}

func ActiveByMode(result ParetoResult, mode string) (Compromise, bool) {
	if len(result.Compromises) == 0 {
		return Compromise{}, false
	}
	wanted := "balanced"
	if mode == "performance" {
		wanted = "performance"
	}
	for i := range result.Compromises {
		if result.Compromises[i].Mode == wanted {
			return result.Compromises[i], true
		}
	}
	return result.Compromises[0], true
}

func buildCompromises(archive []Solution) []Compromise {
	if len(archive) == 0 {
		return nil
	}
	balancedIdx := 0
	performanceIdx := 0
	for i := 1; i < len(archive); i++ {
		if archive[i].Objective.Imbalance < archive[balancedIdx].Objective.Imbalance {
			balancedIdx = i
		}
		if archive[i].Objective.PeakLoad < archive[performanceIdx].Objective.PeakLoad {
			performanceIdx = i
		}
	}

	minF1, maxF1 := archive[0].Objective.Imbalance, archive[0].Objective.Imbalance
	minF2, maxF2 := archive[0].Objective.PeakLoad, archive[0].Objective.PeakLoad
	for i := 1; i < len(archive); i++ {
		f1 := archive[i].Objective.Imbalance
		f2 := archive[i].Objective.PeakLoad
		if f1 < minF1 {
			minF1 = f1
		}
		if f1 > maxF1 {
			maxF1 = f1
		}
		if f2 < minF2 {
			minF2 = f2
		}
		if f2 > maxF2 {
			maxF2 = f2
		}
	}

	type scored struct {
		idx   int
		score float64
	}
	mids := make([]scored, 0, len(archive))
	denF1 := maxFloat(maxF1-minF1, 1e-9)
	denF2 := maxFloat(maxF2-minF2, 1e-9)
	for i := range archive {
		n1 := (archive[i].Objective.Imbalance - minF1) / denF1
		n2 := (archive[i].Objective.PeakLoad - minF2) / denF2
		mids = append(mids, scored{idx: i, score: math.Abs(n1-n2) + (n1 + n2)})
	}
	sort.Slice(mids, func(i, j int) bool { return mids[i].score < mids[j].score })

	middleIdx := mids[0].idx
	used := map[int]bool{balancedIdx: true, performanceIdx: true}
	if used[middleIdx] {
		for _, m := range mids {
			if !used[m.idx] {
				middleIdx = m.idx
				used[m.idx] = true
				break
			}
		}
	}

	out := make([]Compromise, 0, 3)
	out = append(out, Compromise{Mode: "balanced", Solution: archive[balancedIdx]})
	if middleIdx != balancedIdx && middleIdx != performanceIdx {
		out = append(out, Compromise{Mode: "middle", Solution: archive[middleIdx]})
	}
	if performanceIdx != balancedIdx {
		out = append(out, Compromise{Mode: "performance", Solution: archive[performanceIdx]})
	}

	if len(out) < 3 {
		for i := range archive {
			if len(out) >= 3 {
				break
			}
			already := false
			for j := range out {
				if almostEqual(out[j].Solution.Objective.Imbalance, archive[i].Objective.Imbalance) && almostEqual(out[j].Solution.Objective.PeakLoad, archive[i].Objective.PeakLoad) {
					already = true
					break
				}
			}
			if !already {
				out = append(out, Compromise{Mode: "middle", Solution: archive[i]})
			}
		}
	}

	return out
}

func addToArchive(archive []Solution, candidate Solution) []Solution {
	keep := archive[:0]
	for i := range archive {
		if dominates(archive[i].Objective, candidate.Objective) {
			return append(keep, archive[i:]...)
		}
		if !dominates(candidate.Objective, archive[i].Objective) {
			keep = append(keep, archive[i])
		}
	}
	keep = append(keep, candidate)
	if len(keep) <= maxArchive {
		return keep
	}
	sort.Slice(keep, func(i, j int) bool {
		if keep[i].Objective.PeakLoad == keep[j].Objective.PeakLoad {
			return keep[i].Objective.Imbalance < keep[j].Objective.Imbalance
		}
		return keep[i].Objective.PeakLoad < keep[j].Objective.PeakLoad
	})
	return keep[:maxArchive]
}

func dominates(a, b Objective) bool {
	betterOrEqual := a.Imbalance <= b.Imbalance && a.PeakLoad <= b.PeakLoad
	strictlyBetter := a.Imbalance < b.Imbalance || a.PeakLoad < b.PeakLoad
	return betterOrEqual && strictlyBetter
}

func lowerBound(d int) float64 {
	_ = d
	return 0
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

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func clone27(src []float64) []float64 {
	dst := make([]float64, Dimensions)
	copy(dst, src)
	return dst
}

func almostEqual(a, b float64) bool {
	const eps = 1e-9
	return math.Abs(a-b) <= eps
}

func repairParams(params []float64) {
	const eps = 1e-6
	for i := 0; i+2 < len(params); i += 3 {
		a := clamp(params[i], lowerBound(i), upperBound(i))
		b := clamp(params[i+1], lowerBound(i+1), upperBound(i+1))
		c := clamp(params[i+2], lowerBound(i+2), upperBound(i+2))

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

		hi := upperBound(i)
		if c > hi {
			c = hi
			if b > c-eps {
				b = c - eps
			}
			if b < a+eps {
				a = b - eps
			}
		}
		lo := lowerBound(i)
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

// ---- Fast fuzzy scoring (allocation-free path) ----

func fuzzyScore(params []float64, cpu, q, rt float64) float64 {
	muCPU0 := fuzzify(cpu, params[0], params[1], params[2])
	muCPU1 := fuzzify(cpu, params[3], params[4], params[5])
	muCPU2 := fuzzify(cpu, params[6], params[7], params[8])

	muQ0 := fuzzify(q, params[9], params[10], params[11])
	muQ1 := fuzzify(q, params[12], params[13], params[14])
	muQ2 := fuzzify(q, params[15], params[16], params[17])

	muR0 := fuzzify(rt, params[18], params[19], params[20])
	muR1 := fuzzify(rt, params[21], params[22], params[23])
	muR2 := fuzzify(rt, params[24], params[25], params[26])

	cpuVals := [3]float64{muCPU0, muCPU1, muCPU2}
	qVals := [3]float64{muQ0, muQ1, muQ2}
	rVals := [3]float64{muR0, muR1, muR2}
	alphaOut := [3]float64{}

	for i := 0; i < len(compiledRules); i++ {
		r := compiledRules[i]
		a := min3(cpuVals[r.cpu], qVals[r.queue], rVals[r.resp])
		if a > alphaOut[r.out] {
			alphaOut[r.out] = a
		}
	}

	var aTotal, mTotal float64
	for out, alpha := range alphaOut {
		if alpha <= 0 {
			continue
		}
		tri := outMF[out]
		area := alpha * (tri[2] - tri[0]) / 2
		moment := area * (tri[0] + tri[1] + tri[2]) / 3
		aTotal += area
		mTotal += moment
	}
	if aTotal == 0 {
		return 0
	}
	return mTotal / aTotal
}

func fuzzify(v, a, b, c float64) float64 {
	const eps = 1e-6

	// Left shoulder (a ~= b): membership penuh di sisi kiri.
	if math.Abs(b-a) <= eps {
		if v <= b {
			return 1
		}
		if v >= c {
			return 0
		}
		den := c - b
		if den <= eps {
			return 0
		}
		return (c - v) / den
	}

	// Right shoulder (b ~= c): membership penuh di sisi kanan.
	if math.Abs(c-b) <= eps {
		if v >= b {
			return 1
		}
		if v <= a {
			return 0
		}
		den := b - a
		if den <= eps {
			return 0
		}
		return (v - a) / den
	}

	if v == b {
		return 1
	}
	if v <= a || v >= c {
		return 0
	}
	if v < b {
		den := b - a
		if den == 0 {
			return 0
		}
		return (v - a) / den
	}
	den := c - b
	if den == 0 {
		return 0
	}
	return (c - v) / den
}

func min3(a, b, c float64) float64 {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

type ruleIdx struct {
	cpu   int
	queue int
	resp  int
	out   int
}

var compiledRules = [...]ruleIdx{
	{0, 0, 0, 2}, {0, 0, 1, 2}, {0, 0, 2, 1}, {0, 1, 0, 2}, {0, 1, 1, 1}, {0, 1, 2, 1}, {0, 2, 0, 1}, {0, 2, 1, 1}, {0, 2, 2, 0},
	{1, 0, 0, 2}, {1, 0, 1, 1}, {1, 0, 2, 1}, {1, 1, 0, 1}, {1, 1, 1, 1}, {1, 1, 2, 0}, {1, 2, 0, 1}, {1, 2, 1, 0}, {1, 2, 2, 0},
	{2, 0, 0, 1}, {2, 0, 1, 1}, {2, 0, 2, 0}, {2, 1, 0, 1}, {2, 1, 1, 0}, {2, 1, 2, 0}, {2, 2, 0, 0}, {2, 2, 1, 0}, {2, 2, 2, 0},
}

// outMF index: 0=Rendah, 1=Sedang, 2=Tinggi
var outMF = [...][3]float64{{0, 25, 50}, {25, 50, 75}, {50, 75, 100}}
