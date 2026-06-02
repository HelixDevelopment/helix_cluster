package constraints

// Engine resolves a ConstraintSet over a fixed set of resources and candidate
// nodes. It is constructed once and may be re-used; Solve has no hidden state.
type Engine struct {
	resources []Resource
	nodes     []Node
}

// NewEngine builds an Engine. The order of resources and nodes is preserved and
// used as the deterministic tie-break order during scoring.
func NewEngine(resources []Resource, nodes []Node) *Engine {
	return &Engine{resources: resources, nodes: nodes}
}

// Solve resolves cs into a Placement and a start Order.
//
// Algorithm (deterministic):
//  1. Compute the start order from Order constraints via topological sort over
//     resource insertion order; a cycle yields ErrOrderCycle.
//  2. For each resource, build the set of admissible nodes from Location
//     hard-constraints (Required/Forbidden). An empty set yields ErrUnsatisfiable.
//  3. Place resources in start order. For each resource, score every admissible
//     node: + Prefer scores, + stickiness on its Current node, + a large
//     colocationBonus on the node of every already-placed positive-colocation
//     partner, and -inf to veto a node shared with an anti-affinity partner.
//     Pick the highest score (first node wins ties). The bonus dominates any
//     Prefer/stickiness pull, so a resource gravitates to its partner whenever
//     that partner's node is admissible.
//  4. Verify every positive colocation actually ended up satisfied. If a pair
//     could not be co-located (the partner's node was inadmissible for the
//     other member), the set is ErrUnsatisfiable.
func (e *Engine) Solve(cs ConstraintSet) (Result, error) {
	order, err := topoSort(e.resources, cs.Orders)
	if err != nil {
		return Result{}, err
	}

	admissible, err := e.admissibleNodes(cs.Locations)
	if err != nil {
		return Result{}, err
	}

	current := make(map[string]string, len(e.resources))
	stick := make(map[string]int, len(e.resources))
	for _, r := range e.resources {
		current[r.ID] = r.Current
		stick[r.ID] = r.Stickiness
	}

	placement := make(Placement, len(e.resources))

	for _, rid := range order {
		best, ok := e.bestNode(rid, admissible[rid], placement, current, stick, cs)
		if !ok {
			return Result{}, ErrUnsatisfiable
		}
		placement[rid] = best
	}

	// A positive colocation is enforced by colocationBonus, a strong soft pull.
	// If a partner's node was inadmissible for the other member the bonus could
	// not co-locate them, leaving the pair split: that is unsatisfiable.
	for _, co := range cs.Colocations {
		if !co.Together {
			continue
		}
		if placement[co.First] != placement[co.Second] {
			return Result{}, ErrUnsatisfiable
		}
	}

	return Result{Placement: placement, StartOrder: order}, nil
}

const (
	colocationBonus = 1 << 20 // pull a positive-colocated resource onto partner's node
	vetoScore       = -(1 << 30)
)

// bestNode scores every admissible node for rid and returns the winner. A node
// scoring vetoScore is treated as inadmissible. Returns ok=false when no node
// is placeable.
func (e *Engine) bestNode(
	rid string,
	cand []string,
	placement Placement,
	current map[string]string,
	stick map[string]int,
	cs ConstraintSet,
) (string, bool) {
	bestNode := ""
	bestScore := 0
	found := false

	for _, nid := range cand {
		score := 0

		// Location Prefer scores.
		for _, loc := range cs.Locations {
			if loc.Resource == rid && loc.Node == nid && loc.Affinity == Prefer {
				score += loc.Score
			}
		}

		// Stickiness: bonus for staying on the node we currently occupy.
		if cur := current[rid]; cur != "" && cur == nid {
			score += stick[rid]
		}

		// Colocation interactions with already-placed partners.
		vetoed := false
		for _, co := range cs.Colocations {
			partner, involved := colocationPartner(co, rid)
			if !involved {
				continue
			}
			pnode, placed := placement[partner]
			if !placed {
				continue
			}
			if co.Together {
				// Positive colocation: a strong soft pull toward the partner's
				// node. The bonus dominates Prefer/stickiness, so the partner's
				// node wins whenever it is admissible; no veto is used, so an
				// inadmissible partner node degrades to a detectable split
				// (caught by the post-placement check in Solve) rather than a
				// premature per-node veto.
				if pnode == nid {
					score += colocationBonus
				}
			} else {
				// Anti-affinity: veto sharing the partner's node.
				if pnode == nid {
					vetoed = true
				}
			}
		}
		if vetoed {
			continue
		}

		if !found || score > bestScore {
			found = true
			bestScore = score
			bestNode = nid
		}
	}

	return bestNode, found
}

// colocationPartner reports the other resource in co relative to rid, and
// whether rid participates in co at all.
func colocationPartner(co Colocation, rid string) (string, bool) {
	switch rid {
	case co.First:
		return co.Second, true
	case co.Second:
		return co.First, true
	default:
		return "", false
	}
}

// admissibleNodes computes, for every resource, the candidate nodes allowed by
// hard Location constraints. Required pins to exactly the required node(s);
// Forbidden removes a node. An empty result for any resource means the set is
// unsatisfiable.
func (e *Engine) admissibleNodes(locs []Location) (map[string][]string, error) {
	forbidden := make(map[string]map[string]bool)
	required := make(map[string]map[string]bool)
	for _, loc := range locs {
		switch loc.Affinity {
		case Forbidden:
			if forbidden[loc.Resource] == nil {
				forbidden[loc.Resource] = make(map[string]bool)
			}
			forbidden[loc.Resource][loc.Node] = true
		case Required:
			if required[loc.Resource] == nil {
				required[loc.Resource] = make(map[string]bool)
			}
			required[loc.Resource][loc.Node] = true
		}
	}

	out := make(map[string][]string, len(e.resources))
	for _, r := range e.resources {
		var allowed []string
		for _, n := range e.nodes {
			if forbidden[r.ID][n.ID] {
				continue
			}
			if req := required[r.ID]; req != nil && !req[n.ID] {
				continue
			}
			allowed = append(allowed, n.ID)
		}
		if len(allowed) == 0 {
			return nil, ErrUnsatisfiable
		}
		out[r.ID] = allowed
	}
	return out, nil
}

// topoSort returns the resources in a start order honoring every Order edge
// (Before precedes After). Resources not referenced by any edge keep their
// insertion position. A cycle returns ErrOrderCycle.
func topoSort(resources []Resource, orders []Order) ([]string, error) {
	indeg := make(map[string]int)
	adj := make(map[string][]string)
	var ids []string
	seen := make(map[string]bool)
	for _, r := range resources {
		if !seen[r.ID] {
			seen[r.ID] = true
			ids = append(ids, r.ID)
			indeg[r.ID] = 0
		}
	}
	for _, o := range orders {
		adj[o.Before] = append(adj[o.Before], o.After)
		indeg[o.After]++
	}

	// Kahn's algorithm, draining ready nodes in insertion order for determinism.
	var result []string
	for len(result) < len(ids) {
		progressed := false
		for _, id := range ids {
			if indeg[id] == 0 {
				result = append(result, id)
				indeg[id] = -1 // mark consumed
				for _, nxt := range adj[id] {
					indeg[nxt]--
				}
				progressed = true
			}
		}
		if !progressed {
			return nil, ErrOrderCycle
		}
	}
	return result, nil
}
