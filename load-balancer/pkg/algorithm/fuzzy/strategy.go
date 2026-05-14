package fuzzy

import (
	"math/rand"
	"strings"
	"sync/atomic"
	"time"

	lbtypes "load-balancer/pkg/types"
)

type baseFuzzyStrategy struct {
	engine      *Engine
	rules       []Rule
	decisionTag string
	seedCounter atomic.Int64
}

func (s *baseFuzzyStrategy) SelectNode(nodes []lbtypes.BackendNode, snapshot *lbtypes.DecisionSnapshot) *lbtypes.BackendNode {
	if len(nodes) == 0 || s.engine == nil {
		return nil
	}
	if snapshot == nil {
		snapshot = &lbtypes.DecisionSnapshot{}
	}
	if strings.TrimSpace(snapshot.DecisionTag) == "" {
		snapshot.DecisionTag = s.decisionTag
	}

	selectedIdx := 0
	totalScore := 0.0
	scores := make([]float64, len(nodes))

	for i := range nodes {
		normalizedCPU := lbtypes.CalculateNormalizedCPU(nodes[i].CPU, nodes[i].CPUCap)
		score := s.engine.CalculateMamdani(NodeMetrics{
			CPU:         normalizedCPU,
			QueueLength: nodes[i].Queue,
			RespTime:    nodes[i].RespMS,
		}, s.rules)
		if score < 0 {
			score = 0
		}
		scores[i] = score
		totalScore += score
		if i == 0 {
			snapshot.CPU1Normalized = normalizedCPU
			snapshot.Score1 = score
		}
		if i == 1 {
			snapshot.CPU2Normalized = normalizedCPU
			snapshot.Score2 = score
		}
	}

	snapshot.TotalScore = totalScore
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + s.seedCounter.Add(1)))
	if totalScore > 0 {
		roll := rng.Float64() * totalScore
		snapshot.RouletteValue = roll
		cumulative := 0.0
		selectedIdx = len(nodes) - 1
		for i, score := range scores {
			cumulative += score
			if roll <= cumulative {
				selectedIdx = i
				break
			}
		}
	} else {
		roll := rng.Float64()
		snapshot.RouletteValue = roll
		selectedIdx = int(roll * float64(len(nodes)))
		if selectedIdx >= len(nodes) {
			selectedIdx = len(nodes) - 1
		}
	}

	if selectedIdx < 0 || selectedIdx >= len(nodes) {
		return &nodes[0]
	}
	return &nodes[selectedIdx]
}

type FuzzyBaseStrategy struct {
	baseFuzzyStrategy
}

func NewFuzzyBaseStrategy(engine *Engine, rules []Rule) *FuzzyBaseStrategy {
	return &FuzzyBaseStrategy{
		baseFuzzyStrategy: baseFuzzyStrategy{
			engine:      engine,
			rules:       append([]Rule(nil), rules...),
			decisionTag: "FUZZY_BASE",
		},
	}
}

type FuzzyMOPSOStrategy struct {
	baseFuzzyStrategy
}

func NewFuzzyMOPSOStrategy(engine *Engine, rules []Rule) *FuzzyMOPSOStrategy {
	return &FuzzyMOPSOStrategy{
		baseFuzzyStrategy: baseFuzzyStrategy{
			engine:      engine,
			rules:       append([]Rule(nil), rules...),
			decisionTag: "FUZZY_MOPSO",
		},
	}
}
