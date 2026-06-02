package mvcc

// This file implements a real in-memory B-tree (a balanced multi-way search
// tree with node splitting), used as the primary ordered key index of the
// Store. It is deliberately NOT a Go map: the closure requires an ordered index
// so keys can be returned in sorted order (Range) and so per-key history can be
// looked up by an ordered structure. Each B-tree entry maps a string key to a
// pointer to that key's *keyHistory.

// defaultDegree is the minimum degree (t) of the B-tree. Every non-root node
// holds between t-1 and 2t-1 keys. A degree of 4 keeps nodes small enough that
// splits exercise frequently in tests while remaining a valid B-tree.
const defaultDegree = 4

// entry is one (key -> history) pair stored inside a B-tree node.
type entry struct {
	key  string
	hist *keyHistory
}

// node is a single B-tree node. entries are kept sorted ascending by key.
// children has len(entries)+1 elements for an internal node, or is nil/empty
// for a leaf.
type node struct {
	leaf     bool
	entries  []entry
	children []*node
}

// btree is the ordered index.
type btree struct {
	root   *node
	degree int
}

func newBtree(degree int) *btree {
	if degree < 2 {
		degree = 2
	}
	return &btree{
		root:   &node{leaf: true},
		degree: degree,
	}
}

// search returns the index of key within n.entries and whether it was found.
// When not found, the returned index is the position where the key would be
// inserted (i.e. the first entry with key strictly greater).
func (n *node) search(key string) (int, bool) {
	lo, hi := 0, len(n.entries)
	for lo < hi {
		mid := (lo + hi) / 2
		if n.entries[mid].key < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(n.entries) && n.entries[lo].key == key {
		return lo, true
	}
	return lo, false
}

// get returns the history for key, or nil if absent.
func (t *btree) get(key string) *keyHistory {
	n := t.root
	for n != nil {
		idx, found := n.search(key)
		if found {
			return n.entries[idx].hist
		}
		if n.leaf {
			return nil
		}
		n = n.children[idx]
	}
	return nil
}

// insert adds key->hist. If key already exists its history pointer is left
// unchanged (callers append versions in place via the returned pointer from
// get). insert maintains B-tree balance by splitting full nodes.
func (t *btree) insert(key string, hist *keyHistory) {
	r := t.root
	if len(r.entries) == 2*t.degree-1 {
		// Root is full: grow height by one.
		s := &node{leaf: false, children: []*node{r}}
		s.splitChild(0, t.degree)
		t.root = s
	}
	t.insertNonFull(t.root, key, hist)
}

// splitChild splits the full child at index i of parent n. The median entry of
// the child moves up into n; the child is divided into two nodes.
func (n *node) splitChild(i, degree int) {
	full := n.children[i]
	mid := degree - 1

	right := &node{leaf: full.leaf}
	// right gets the entries after the median.
	right.entries = append(right.entries, full.entries[mid+1:]...)
	median := full.entries[mid]

	if !full.leaf {
		right.children = append(right.children, full.children[mid+1:]...)
		full.children = full.children[:mid+1]
	}
	full.entries = full.entries[:mid]

	// Insert median into n at position i, and right child at position i+1.
	n.entries = append(n.entries, entry{})
	copy(n.entries[i+1:], n.entries[i:])
	n.entries[i] = median

	n.children = append(n.children, nil)
	copy(n.children[i+2:], n.children[i+1:])
	n.children[i+1] = right
}

// insertNonFull inserts into a node guaranteed not to be full.
func (t *btree) insertNonFull(n *node, key string, hist *keyHistory) {
	idx, found := n.search(key)
	if found {
		// Key already present: keep existing history pointer.
		return
	}
	if n.leaf {
		n.entries = append(n.entries, entry{})
		copy(n.entries[idx+1:], n.entries[idx:])
		n.entries[idx] = entry{key: key, hist: hist}
		return
	}
	// Descend into child idx, splitting it first if full.
	if len(n.children[idx].entries) == 2*t.degree-1 {
		n.splitChild(idx, t.degree)
		// After split the median rose into n at position idx; decide direction.
		if key > n.entries[idx].key {
			idx++
		} else if key == n.entries[idx].key {
			return
		}
	}
	t.insertNonFull(n.children[idx], key, hist)
}

// rangeKeys returns keys in [start, end) in ascending order via an in-order
// traversal. An empty end means unbounded above.
func (t *btree) rangeKeys(start, end string) []string {
	out := make([]string, 0)
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.leaf {
			for _, e := range n.entries {
				if e.key < start {
					continue
				}
				if end != "" && e.key >= end {
					continue
				}
				out = append(out, e.key)
			}
			return
		}
		for i := range n.entries {
			walk(n.children[i])
			k := n.entries[i].key
			if k >= start && (end == "" || k < end) {
				out = append(out, k)
			}
		}
		walk(n.children[len(n.entries)])
	}
	walk(t.root)
	return out
}

// allHistories returns every history pointer in the tree (order unspecified).
func (t *btree) allHistories() []*keyHistory {
	out := make([]*keyHistory, 0)
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		for i := range n.entries {
			if !n.leaf {
				walk(n.children[i])
			}
			out = append(out, n.entries[i].hist)
		}
		if !n.leaf {
			walk(n.children[len(n.entries)])
		}
	}
	walk(t.root)
	return out
}
