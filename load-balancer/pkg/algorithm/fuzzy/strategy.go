package fuzzy

import (
	"strings"

	lbtypes "load-balancer/pkg/types"
)

type baseFuzzyStrategy struct {
	engine      *Engine
	rules       []Rule
	decisionTag string
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

	bestIdx := 0
	bestScore := -1.0
	totalScore := 0.0

	for i := range nodes {
		score := s.engine.CalculateMamdani(NodeMetrics{
			CPU:         nodes[i].CPU,
			QueueLength: nodes[i].Queue,
			RespTime:    nodes[i].RespMS,
		}, s.rules)
		if score < 0 {
			score = 0
		}
		totalScore += score
		if i == 0 {
			snapshot.Score1 = score
		}
		if i == 1 {
			snapshot.Score2 = score
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	snapshot.TotalScore = totalScore
	snapshot.RouletteValue = -1
	if bestIdx < 0 || bestIdx >= len(nodes) {
		return &nodes[0]
	}
	return &nodes[bestIdx]
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
