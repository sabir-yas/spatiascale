package spatial

import (
	"fmt"
	"sync"
	"sync/atomic"
)

const (
	DefaultCapacity = 64 // max points per leaf node before splitting
	DefaultMaxDepth = 20 // deepest the tree can grow
)

// nodeState tracks whether a node is a leaf, has been split into children,
// or has been evicted to Redis (stub only, data lives in cache).
type nodeState uint8

const (
	stateLeaf     nodeState = iota // holds points directly
	stateInternal                  // split into 4 children
	stateEvicted                   // data offloaded to Redis
)

// quadNode is one node in the tree. It's either a leaf (holds points)
// or internal (holds 4 child nodes). Never both at the same time.
type quadNode struct {
	mu       sync.RWMutex
	bounds   BoundingBox
	state    nodeState
	points   []Point     // non-nil only when stateLeaf
	children [4]*quadNode // non-nil only when stateInternal
	cacheKey string      // set when stateEvicted
	depth    uint8
	count    int64 // total points in this subtree (atomic)
}

// QuadTree is the top-level concurrent spatial index.
// Multiple goroutines can query simultaneously (read locks).
// Inserts and splits use write locks scoped to individual nodes,
// so two inserts into different branches never block each other.
type QuadTree struct {
	root     *quadNode
	capacity int
	maxDepth uint8

	// evictor is called when a cold node should be pushed to Redis.
	// Nil means eviction is disabled (Phase 1 — no Redis yet).
	evictor func(node *quadNode) error
	loader  func(cacheKey string) (*quadNode, error)
}

// NewQuadTree creates a tree covering the given bounds.
func NewQuadTree(bounds BoundingBox) *QuadTree {
	return &QuadTree{
		root: &quadNode{
			bounds: bounds,
			state:  stateLeaf,
			points: make([]Point, 0, DefaultCapacity),
		},
		capacity: DefaultCapacity,
		maxDepth: DefaultMaxDepth,
	}
}

// Insert adds a point to the tree. Thread-safe — multiple goroutines
// can call Insert concurrently.
func (qt *QuadTree) Insert(p Point) error {
	if !qt.root.bounds.Contains(p) {
		return fmt.Errorf("point (%.4f, %.4f) out of tree bounds", p.X, p.Y)
	}
	return qt.insert(qt.root, p)
}

func (qt *QuadTree) insert(node *quadNode, p Point) error {
	// Fast path: read lock to check state before committing to a write.
	node.mu.RLock()
	state := node.state
	node.mu.RUnlock()

	switch state {
	case stateInternal:
		// Recurse into the child whose bounds contain this point.
		child := qt.childFor(node, p)
		if child == nil {
			return fmt.Errorf("no child found for point (%.4f, %.4f)", p.X, p.Y)
		}
		if err := qt.insert(child, p); err != nil {
			return err
		}
		atomic.AddInt64(&node.count, 1)
		return nil

	case stateLeaf:
		node.mu.Lock()

		// Re-check state under write lock — another goroutine may have
		// already split this node between our RLock and Lock above.
		if node.state == stateInternal {
			node.mu.Unlock()
			return qt.insert(node, p)
		}

		node.points = append(node.points, p)
		atomic.AddInt64(&node.count, 1)

		// Split if over capacity and not at max depth.
		if len(node.points) > qt.capacity && node.depth < qt.maxDepth {
			qt.split(node)
		}
		node.mu.Unlock()
		return nil

	case stateEvicted:
		// Node was evicted to Redis. Reload it first (Phase 2).
		if qt.loader == nil {
			return fmt.Errorf("node is evicted but no loader configured")
		}
		loaded, err := qt.loader(node.cacheKey)
		if err != nil {
			return fmt.Errorf("reload evicted node: %w", err)
		}
		node.mu.Lock()
		node.points = loaded.points
		node.state = stateLeaf
		node.cacheKey = ""
		node.mu.Unlock()
		return qt.insert(node, p)
	}
	return nil
}

// split converts a leaf node into an internal node by distributing its
// points among 4 child nodes. Caller must hold node.mu write lock.
func (qt *QuadTree) split(node *quadNode) {
	quads := node.bounds.Subdivide()
	for i := range node.children {
		node.children[i] = &quadNode{
			bounds: quads[i],
			state:  stateLeaf,
			points: make([]Point, 0, qt.capacity),
			depth:  node.depth + 1,
		}
	}
	// Redistribute existing points into children.
	for _, p := range node.points {
		for _, child := range node.children {
			if child.bounds.Contains(p) {
				child.points = append(child.points, p)
				atomic.AddInt64(&child.count, 1)
				break
			}
		}
	}
	// Transition to internal: clear leaf data.
	node.points = nil
	node.state = stateInternal
}

// childFor returns the child node whose bounds contain point p.
// Caller must NOT hold node.mu (we take an RLock internally).
func (qt *QuadTree) childFor(node *quadNode, p Point) *quadNode {
	node.mu.RLock()
	defer node.mu.RUnlock()
	for _, child := range node.children {
		if child != nil && child.bounds.Contains(p) {
			return child
		}
	}
	return nil
}

// RangeQuery returns all points whose coordinates fall within bb.
// Multiple goroutines can call RangeQuery concurrently.
func (qt *QuadTree) RangeQuery(bb BoundingBox) ([]Point, error) {
	results := make([]Point, 0, 64)
	err := qt.rangeQuery(qt.root, bb, &results)
	return results, err
}

func (qt *QuadTree) rangeQuery(node *quadNode, bb BoundingBox, results *[]Point) error {
	node.mu.RLock()
	defer node.mu.RUnlock()

	// Pruning: if this node's bounds don't overlap the query box,
	// skip the entire subtree — this is the key performance optimization.
	if !node.bounds.Intersects(bb) {
		return nil
	}

	switch node.state {
	case stateLeaf:
		for _, p := range node.points {
			if bb.Contains(p) {
				*results = append(*results, p)
			}
		}

	case stateInternal:
		for _, child := range node.children {
			if child != nil {
				if err := qt.rangeQuery(child, bb, results); err != nil {
					return err
				}
			}
		}

	case stateEvicted:
		if qt.loader == nil {
			return fmt.Errorf("node evicted but no loader configured")
		}
		// Release read lock before loading (loader may need a write lock).
		node.mu.RUnlock()
		loaded, err := qt.loader(node.cacheKey)
		node.mu.RLock()
		if err != nil {
			return fmt.Errorf("reload evicted node: %w", err)
		}
		for _, p := range loaded.points {
			if bb.Contains(p) {
				*results = append(*results, p)
			}
		}
	}
	return nil
}

// Count returns the total number of points in the tree.
func (qt *QuadTree) Count() int64 {
	return atomic.LoadInt64(&qt.root.count)
}

// PartitionCount returns the number of leaf nodes (partitions).
func (qt *QuadTree) PartitionCount() int {
	return countLeaves(qt.root)
}

func countLeaves(node *quadNode) int {
	node.mu.RLock()
	defer node.mu.RUnlock()
	if node.state == stateLeaf || node.state == stateEvicted {
		return 1
	}
	total := 0
	for _, child := range node.children {
		if child != nil {
			total += countLeaves(child)
		}
	}
	return total
}
