package roundrobin

import lbtypes "load-balancer/pkg/types"

type RoundRobinStrategy struct {
	balancer *Balancer
}

func NewRoundRobinStrategy() *RoundRobinStrategy {
	return &RoundRobinStrategy{balancer: New()}
}

func (s *RoundRobinStrategy) SelectNode(nodes []lbtypes.BackendNode, snapshot *lbtypes.DecisionSnapshot) *lbtypes.BackendNode {
	if len(nodes) == 0 {
		return nil
	}
	idx := s.balancer.NextIndex(len(nodes))
	if snapshot != nil {
		snapshot.DecisionTag = "ROUND_ROBIN"
		snapshot.TotalScore = 0
		snapshot.RouletteValue = -1
	}
	if idx < 0 {
		return nil
	}
	return &nodes[idx]
}
