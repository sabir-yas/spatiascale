package spatial

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
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
	mu         sync.RWMutex
	bounds     BoundingBox
	state      nodeState
	points     []Point      // non-nil only when stateLeaf
	children   [4]*quadNode // non-nil only when stateInternal
	cacheKey   string       // set when stateEvicted
	depth      uint8
	count      int64 // total points in this subtree (atomic)
	lastAccess int64 // unix nanoseconds of last insert/query touch (atomic)
}

// touch records that this leaf was just read or written, for idle-based
// eviction decisions.
func (n *quadNode) touch() {
	atomic.StoreInt64(&n.lastAccess, time.Now().UnixNano())
}

// Cache is the interface a QuadTree uses to offload cold leaf nodes out of
// process memory (e.g. to Redis) and reload them on demand. Decoupling the
// tree from any specific backend keeps internal/spatial free of Redis
// (or any other store) dependencies.
type Cache interface {
	Store(key string, points []Point) error
	Load(key string) ([]Point, error)
	Delete(key string) error
}

// QuadTree is the top-level concurrent spatial index.
// Multiple goroutines can query simultaneously (read locks).
// Inserts and splits use write locks scoped to individual nodes,
// so two inserts into different branches never block each other.
type QuadTree struct {
	root     *quadNode
	capacity int
	maxDepth uint8

	// cache handles evicting cold leaves out of memory and reloading them.
	// Nil means eviction is disabled.
	cache    Cache
	cacheSeq uint64 // atomic counter for generating unique cache keys
}

// NewQuadTree creates a tree covering the given bounds.
func NewQuadTree(bounds BoundingBox) *QuadTree {
	root := &quadNode{
		bounds: bounds,
		state:  stateLeaf,
		points: make([]Point, 0, DefaultCapacity),
	}
	root.touch()
	return &QuadTree{
		root:     root,
		capacity: DefaultCapacity,
		maxDepth: DefaultMaxDepth,
	}
}

// SetCache attaches a Cache backend, enabling EvictIdle. Not safe to call
// concurrently with tree mutations.
func (qt *QuadTree) SetCache(c Cache) {
	qt.cache = c
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
		node.touch()

		// Split if over capacity and not at max depth.
		if len(node.points) > qt.capacity && node.depth < qt.maxDepth {
			qt.split(node)
		}
		node.mu.Unlock()
		return nil

	case stateEvicted:
		if err := qt.reload(node); err != nil {
			return err
		}
		return qt.insert(node, p)
	}
	return nil
}

// reload fetches an evicted node's points back from the cache and restores
// it to stateLeaf. Caller must NOT hold node.mu.
func (qt *QuadTree) reload(node *quadNode) error {
	if qt.cache == nil {
		return fmt.Errorf("node is evicted but no cache configured")
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.state != stateEvicted {
		return nil // another goroutine already reloaded it
	}
	points, err := qt.cache.Load(node.cacheKey)
	if err != nil {
		return fmt.Errorf("reload evicted node: %w", err)
	}
	node.points = points
	node.state = stateLeaf
	node.cacheKey = ""
	node.touch()
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
		node.children[i].touch()
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
		node.touch()
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
		// Release read lock before reload (which needs a write lock).
		node.mu.RUnlock()
		err := qt.reload(node)
		node.mu.RLock()
		if err != nil {
			return err
		}
		for _, p := range node.points {
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

// EvictIdle walks the tree and evicts every leaf whose last insert/query
// touch is older than idleFor, pushing its points to the cache and freeing
// them from process memory. Requires SetCache to have been called first.
// Returns the number of leaves evicted.
func (qt *QuadTree) EvictIdle(idleFor time.Duration) (int, error) {
	if qt.cache == nil {
		return 0, fmt.Errorf("EvictIdle: no cache configured, call SetCache first")
	}
	cutoff := time.Now().Add(-idleFor).UnixNano()
	return qt.evictIdle(qt.root, cutoff)
}

func (qt *QuadTree) evictIdle(node *quadNode, cutoff int64) (int, error) {
	node.mu.RLock()
	state := node.state
	node.mu.RUnlock()

	if state == stateInternal {
		total := 0
		for _, child := range node.children {
			if child != nil {
				n, err := qt.evictIdle(child, cutoff)
				total += n
				if err != nil {
					return total, err
				}
			}
		}
		return total, nil
	}

	if state != stateLeaf {
		return 0, nil // already evicted
	}

	node.mu.Lock()
	defer node.mu.Unlock()
	if node.state != stateLeaf || atomic.LoadInt64(&node.lastAccess) > cutoff || len(node.points) == 0 {
		return 0, nil
	}

	key := fmt.Sprintf("quadtree-node-%d", atomic.AddUint64(&qt.cacheSeq, 1))
	if err := qt.cache.Store(key, node.points); err != nil {
		return 0, fmt.Errorf("evict node: %w", err)
	}
	node.points = nil
	node.cacheKey = key
	node.state = stateEvicted
	return 1, nil
}
