// Package fedtopology models the five canonical federation topology patterns
// for Helix Cluster inter-cell adjacency and validates their structural
// invariants.
//
// Supported patterns:
//   - HubSpoke  – a single hub connected to every other (spoke) cell; no spoke-spoke edges.
//   - FullMesh  – every pair of cells is directly connected.
//   - Ring      – each cell connects to exactly two neighbours, forming one cycle.
//   - Tree      – rooted at hub; n-1 edges, connected, acyclic (built with BFS fanout).
//   - Hierarchical – multi-level rooted tree with branching factor k=ceil(sqrt(n)).
//     For the same input, Hierarchical produces a wider, shallower tree than Tree
//     (e.g. n=9 → k=3; Tree uses k=2).  Invariants: n-1 edges, connected, acyclic.
package fedtopology

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Pattern identifies one of the five canonical federation topology patterns.
type Pattern uint8

const (
	// HubSpoke connects every non-hub cell exclusively to a single hub cell.
	// Hub degree == n-1; spoke degree == 1; no spoke-spoke edges.
	HubSpoke Pattern = iota
	// FullMesh connects every pair of cells directly.
	// Each cell has degree n-1; total edge count == n*(n-1)/2.
	FullMesh
	// Ring connects cells in a single closed cycle.
	// Each cell has exactly 2 neighbours.
	Ring
	// Tree builds a BFS-ordered rooted tree from hub.
	// Invariants: n-1 edges, connected, acyclic.
	Tree
	// Hierarchical builds a multi-level rooted tree with branching factor
	// ceil(sqrt(n)), where n is the total cell count.  For n == 9 the factor
	// is 3, producing a shallower, wider tree than Tree's binary fanout.
	// Invariants: n-1 edges, connected, acyclic.
	Hierarchical
)

// String returns the human-readable name of the pattern.
func (p Pattern) String() string {
	switch p {
	case HubSpoke:
		return "HubSpoke"
	case FullMesh:
		return "FullMesh"
	case Ring:
		return "Ring"
	case Tree:
		return "Tree"
	case Hierarchical:
		return "Hierarchical"
	default:
		return fmt.Sprintf("Pattern(%d)", uint8(p))
	}
}

// Topology holds the adjacency representation of a built federation topology.
// The graph is undirected; each edge is stored in both directions.
type Topology struct {
	// Pattern is the topology pattern used to build this topology.
	Pattern Pattern
	// Hub is the designated hub or root cell (used by HubSpoke, Tree, Hierarchical).
	// Empty for FullMesh and Ring.
	Hub string
	// adj maps each cell ID to the set of its neighbours.
	adj map[string]map[string]struct{}
}

// Build constructs a Topology for the given pattern over the provided cell IDs.
//
// The hub parameter is the designated centre cell for HubSpoke and the root for
// Tree/Hierarchical; it is ignored (and may be empty) for FullMesh and Ring.
//
// Errors are returned for:
//   - fewer than 2 cells
//   - duplicate cell IDs
//   - hub not present in cells (when required: HubSpoke, Tree, Hierarchical)
func Build(pattern Pattern, cells []string, hub string) (*Topology, error) {
	if len(cells) < 2 {
		return nil, fmt.Errorf("fedtopology: need at least 2 cells, got %d", len(cells))
	}

	// Duplicate detection.
	seen := make(map[string]struct{}, len(cells))
	for _, c := range cells {
		if _, dup := seen[c]; dup {
			return nil, fmt.Errorf("fedtopology: duplicate cell %q", c)
		}
		seen[c] = struct{}{}
	}

	// Hub validation where required.
	requiresHub := pattern == HubSpoke || pattern == Tree || pattern == Hierarchical
	if requiresHub {
		if _, ok := seen[hub]; !ok {
			return nil, fmt.Errorf("fedtopology: hub %q not found in cells", hub)
		}
	}

	// Initialise adjacency sets.
	adj := make(map[string]map[string]struct{}, len(cells))
	for _, c := range cells {
		adj[c] = make(map[string]struct{})
	}

	addEdge := func(a, b string) {
		adj[a][b] = struct{}{}
		adj[b][a] = struct{}{}
	}

	// Produce a stable, sorted slice of cells for deterministic construction.
	sorted := make([]string, len(cells))
	copy(sorted, cells)
	sort.Strings(sorted)

	switch pattern {
	case HubSpoke:
		for _, c := range sorted {
			if c == hub {
				continue
			}
			addEdge(hub, c)
		}

	case FullMesh:
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				addEdge(sorted[i], sorted[j])
			}
		}

	case Ring:
		// Connect sorted[0]-sorted[1]-...-sorted[n-1]-sorted[0].
		n := len(sorted)
		for i := 0; i < n; i++ {
			addEdge(sorted[i], sorted[(i+1)%n])
		}

	case Tree:
		// BFS from hub over the sorted cell list (hub first, others in order).
		// Place hub at position 0 in the BFS queue; others follow in sorted order.
		order := treeOrder(sorted, hub)
		buildBFSTree(order, addEdge)

	case Hierarchical:
		// Multi-level k-ary tree with branching factor k = ceil(sqrt(n)).
		// For n=9 this gives k=3 (3-ary), producing a strictly wider and
		// shallower tree than the binary Tree pattern for the same input.
		order := treeOrder(sorted, hub)
		k := branchingFactor(len(order))
		buildKAryTree(k, order, addEdge)
	}

	return &Topology{
		Pattern: pattern,
		Hub:     hub,
		adj:     adj,
	}, nil
}

// treeOrder returns a slice with hub first, then the remaining cells in sorted order.
func treeOrder(sorted []string, hub string) []string {
	order := make([]string, 0, len(sorted))
	order = append(order, hub)
	for _, c := range sorted {
		if c != hub {
			order = append(order, c)
		}
	}
	return order
}

// buildBFSTree adds binary-tree edges to adj over order (order[0] is root).
// Node at index i has parent at index (i-1)/2, giving a balanced binary tree
// (branching factor 2).  Used exclusively by the Tree pattern.
//
// Deterministic: order is already sorted with hub at index 0.
func buildBFSTree(order []string, addEdge func(a, b string)) {
	for i := 1; i < len(order); i++ {
		parent := order[(i-1)/2]
		addEdge(parent, order[i])
	}
}

// buildKAryTree adds k-ary tree edges to adj over order (order[0] is root).
// Node at index i has parent at index (i-1)/k, giving a balanced k-ary tree.
// Used by the Hierarchical pattern with k = branchingFactor(n).
//
// Deterministic: order is already sorted with hub at index 0.
func buildKAryTree(k int, order []string, addEdge func(a, b string)) {
	if k < 2 {
		k = 2 // degenerate-safety: never produce a degenerate chain
	}
	for i := 1; i < len(order); i++ {
		parent := order[(i-1)/k]
		addEdge(parent, order[i])
	}
}

// Neighbors returns the sorted list of cells adjacent to the given cell.
// Returns nil if the cell is not part of the topology.
func (t *Topology) Neighbors(cell string) []string {
	nbrs, ok := t.adj[cell]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(nbrs))
	for n := range nbrs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Edges returns all undirected edges of the topology, sorted lexicographically.
// Each edge [2]string{a, b} satisfies a < b.
func (t *Topology) Edges() [][2]string {
	var edges [][2]string
	for a, nbrs := range t.adj {
		for b := range nbrs {
			if a < b {
				edges = append(edges, [2]string{a, b})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] != edges[j][0] {
			return edges[i][0] < edges[j][0]
		}
		return edges[i][1] < edges[j][1]
	})
	return edges
}

// CanBind reports whether an inter-cell binding between a and b is permitted
// by this topology (i.e. the edge exists in the adjacency graph).
func (t *Topology) CanBind(a, b string) bool {
	nbrs, ok := t.adj[a]
	if !ok {
		return false
	}
	_, ok = nbrs[b]
	return ok
}

// Validate checks that the topology satisfies its structural invariant.
//
//   - HubSpoke:    hub degree == n-1; every spoke degree == 1; no spoke-spoke edge.
//   - FullMesh:    every cell degree == n-1; edge count == n*(n-1)/2.
//   - Ring:        every cell degree == 2; graph is connected (single cycle).
//   - Tree:        n-1 edges; graph connected and acyclic.
//   - Hierarchical: same as Tree (n-1 edges; connected; acyclic).
func (t *Topology) Validate() error {
	n := len(t.adj)
	edges := t.Edges()

	switch t.Pattern {
	case HubSpoke:
		return validateHubSpoke(t, n)
	case FullMesh:
		return validateFullMesh(t, n, edges)
	case Ring:
		return validateRing(t, n, edges)
	case Tree, Hierarchical:
		return validateTree(t, n, edges)
	default:
		return fmt.Errorf("fedtopology: unknown pattern %d", t.Pattern)
	}
}

// validateHubSpoke checks: hub degree n-1; every non-hub degree 1; no spoke-spoke.
func validateHubSpoke(t *Topology, n int) error {
	hubNbrs := t.adj[t.Hub]
	if len(hubNbrs) != n-1 {
		return fmt.Errorf("fedtopology: HubSpoke hub %q degree=%d want %d",
			t.Hub, len(hubNbrs), n-1)
	}
	var errs []string
	for cell, nbrs := range t.adj {
		if cell == t.Hub {
			continue
		}
		if len(nbrs) != 1 {
			errs = append(errs, fmt.Sprintf("spoke %q has degree %d (want 1)", cell, len(nbrs)))
		}
		// Check for spoke-spoke edges.
		for nb := range nbrs {
			if nb != t.Hub {
				errs = append(errs, fmt.Sprintf("spoke %q has spoke-neighbour %q", cell, nb))
			}
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return errors.New("fedtopology: HubSpoke invariant violated: " + strings.Join(errs, "; "))
	}
	return nil
}

// validateFullMesh checks: every cell degree n-1; edge count n*(n-1)/2.
func validateFullMesh(t *Topology, n int, edges [][2]string) error {
	wantEdges := n * (n - 1) / 2
	if len(edges) != wantEdges {
		return fmt.Errorf("fedtopology: FullMesh edge count=%d want %d", len(edges), wantEdges)
	}
	var errs []string
	for cell, nbrs := range t.adj {
		if len(nbrs) != n-1 {
			errs = append(errs, fmt.Sprintf("cell %q degree=%d want %d", cell, len(nbrs), n-1))
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return errors.New("fedtopology: FullMesh invariant violated: " + strings.Join(errs, "; "))
	}
	return nil
}

// validateRing checks: every cell degree 2; graph connected.
func validateRing(t *Topology, n int, edges [][2]string) error {
	var errs []string
	for cell, nbrs := range t.adj {
		if len(nbrs) != 2 {
			errs = append(errs, fmt.Sprintf("cell %q degree=%d want 2", cell, len(nbrs)))
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return errors.New("fedtopology: Ring invariant violated (degrees): " + strings.Join(errs, "; "))
	}
	// Connectivity check via BFS.
	if err := checkConnected(t); err != nil {
		return fmt.Errorf("fedtopology: Ring invariant violated: %w", err)
	}
	// Edge count for a ring == n.
	if len(edges) != n {
		return fmt.Errorf("fedtopology: Ring edge count=%d want %d", len(edges), n)
	}
	return nil
}

// validateTree checks: n-1 edges; connected; acyclic.
func validateTree(t *Topology, n int, edges [][2]string) error {
	if len(edges) != n-1 {
		return fmt.Errorf("fedtopology: %s edge count=%d want %d",
			t.Pattern, len(edges), n-1)
	}
	if err := checkConnected(t); err != nil {
		return fmt.Errorf("fedtopology: %s invariant violated: %w", t.Pattern, err)
	}
	if err := checkAcyclic(t); err != nil {
		return fmt.Errorf("fedtopology: %s invariant violated: %w", t.Pattern, err)
	}
	return nil
}

// checkConnected performs a BFS from an arbitrary cell and reports an error if
// not all cells are reachable.
func checkConnected(t *Topology) error {
	// Pick a deterministic start node.
	var start string
	for c := range t.adj {
		start = c
		break
	}
	visited := make(map[string]bool, len(t.adj))
	queue := []string{start}
	visited[start] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for nb := range t.adj[cur] {
			if !visited[nb] {
				visited[nb] = true
				queue = append(queue, nb)
			}
		}
	}
	if len(visited) != len(t.adj) {
		return fmt.Errorf("graph not connected: reached %d/%d cells", len(visited), len(t.adj))
	}
	return nil
}

// checkAcyclic uses DFS to detect cycles in the undirected graph.
func checkAcyclic(t *Topology) error {
	visited := make(map[string]bool, len(t.adj))
	// Stable DFS order.
	cells := make([]string, 0, len(t.adj))
	for c := range t.adj {
		cells = append(cells, c)
	}
	sort.Strings(cells)

	var dfs func(cur, parent string) error
	dfs = func(cur, parent string) error {
		visited[cur] = true
		// Visit neighbours in sorted order for determinism.
		nbrs := make([]string, 0, len(t.adj[cur]))
		for nb := range t.adj[cur] {
			nbrs = append(nbrs, nb)
		}
		sort.Strings(nbrs)
		for _, nb := range nbrs {
			if nb == parent {
				continue
			}
			if visited[nb] {
				return fmt.Errorf("cycle detected at %q -> %q", cur, nb)
			}
			if err := dfs(nb, cur); err != nil {
				return err
			}
		}
		return nil
	}

	for _, c := range cells {
		if !visited[c] {
			if err := dfs(c, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// branchingFactor returns ceil(sqrt(n)), the k-ary fanout used by the
// Hierarchical pattern.  For n=4 → 2, n=9 → 3, n=16 → 4.
func branchingFactor(n int) int {
	return int(math.Ceil(math.Sqrt(float64(n))))
}
