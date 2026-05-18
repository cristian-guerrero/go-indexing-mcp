package storage

import (
	"container/heap"
	"math"
)

// coverPoint represents a vector in the cover tree.
type coverPoint struct {
	Vector    []float32
	Magnitude float32
	index     int32
}

// DistanceFunc computes the distance between two cover tree points.
type DistanceFunc func(p1, p2 *coverPoint) float32

// CosineDistance returns the cosine distance (1 - cosine similarity).
// Uses dotProduct32 for the dot product; handles zero-magnitude vectors.
func CosineDistance(p1, p2 *coverPoint) float32 {
	dot := dotProduct32(p1.Vector, p2.Vector)
	m1 := p1.Magnitude
	if m1 == 0 {
		var sum float64
		for _, v := range p1.Vector {
			sum += float64(v) * float64(v)
		}
		m1 = float32(math.Sqrt(sum))
	}
	m2 := p2.Magnitude
	if m2 == 0 {
		var sum float64
		for _, v := range p2.Vector {
			sum += float64(v) * float64(v)
		}
		m2 = float32(math.Sqrt(sum))
	}
	denom := m1 * m2
	if denom == 0 {
		return 1.0
	}
	return 1.0 - float32(float64(dot)/float64(denom))
}

// coverNode represents a node in the cover tree.
type coverNode struct {
	level          int32
	baseLevel      float32
	point          *coverPoint
	children       []coverNode
	radius         float32
	radiusComputed uint64
}

// newCoverNode constructs a node for the provided point and level.
func newCoverNode(point *coverPoint, level int32, base float32) coverNode {
	return coverNode{
		level:     level,
		baseLevel: float32(math.Pow(float64(base), float64(level))),
		point:     point,
	}
}

// coverTree implements a cover tree for exact/approximate kNN search.
// Adapted from sqlite-vec's internal/cover/tree without generics or serialization.
type coverTree struct {
	root         *coverNode
	base         float32
	distanceFunc DistanceFunc
	version      uint64
}

// newCoverTree creates a cover tree with the provided base (must be >1) and distance metric.
func newCoverTree(base float32, distanceFunc DistanceFunc) *coverTree {
	if base <= 1 {
		base = 1.3
	}
	return &coverTree{
		base:         base,
		distanceFunc: distanceFunc,
	}
}

// Insert adds a point to the tree.
func (t *coverTree) Insert(point *coverPoint) {
	if point.Magnitude == 0 && len(point.Vector) > 0 {
		var sum float64
		for _, v := range point.Vector {
			sum += float64(v) * float64(v)
		}
		point.Magnitude = float32(math.Sqrt(sum))
	}
	if t.root == nil {
		node := coverNode{level: 0, baseLevel: 1, point: point}
		t.root = &node
	} else {
		t.insert(t.root, point)
	}
	t.version++
}

func (t *coverTree) insert(node *coverNode, point *coverPoint) {
	baseLevel := node.baseLevel

	for {
		distance := t.distanceFunc(point, node.point)
		if distance < baseLevel {
			inserted := false
			for i := range node.children {
				child := &node.children[i]
				if t.distanceFunc(point, child.point) < baseLevel {
					node = child
					baseLevel = node.baseLevel
					inserted = true
					break
				}
			}
			if !inserted {
				childLevel := node.level - 1
				childBase := baseLevel / t.base
				node.children = append(node.children, coverNode{
					level: childLevel,
					baseLevel: childBase,
					point: point,
				})
				return
			}
		} else {
			newLevel := node.level + 1
			newBaseLevel := baseLevel * t.base
			newRoot := coverNode{level: newLevel, baseLevel: newBaseLevel, point: point}
			newRoot.children = append(newRoot.children, *t.root)
			t.root = &newRoot
			return
		}
	}
}

// neighbor describes a search result from the cover tree.
type neighbor struct {
	point    *coverPoint
	distance float32
}

// neighbors implements heap.Interface sorted by descending distance (max-heap).
type neighbors []neighbor

func (h neighbors) Len() int           { return len(h) }
func (h neighbors) Less(i, j int) bool { return h[i].distance > h[j].distance }
func (h neighbors) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *neighbors) Push(x interface{}) {
	*h = append(*h, x.(neighbor))
}

func (h *neighbors) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// ensureRadius computes and caches the per-node subtree radius (tightest bound).
func (t *coverTree) ensureRadius(n *coverNode) float32 {
	if n == nil {
		return 0
	}
	if n.radiusComputed == t.version {
		return n.radius
	}
	if len(n.children) == 0 {
		n.radius = 0
		n.radiusComputed = t.version
		return 0
	}
	maxR := float32(0)
	for i := range n.children {
		child := &n.children[i]
		cr := t.ensureRadius(child)
		d := t.distanceFunc(n.point, child.point) + cr
		if d > maxR {
			maxR = d
		}
	}
	n.radius = maxR
	n.radiusComputed = t.version
	return maxR
}

func (t *coverTree) boundRadius(n *coverNode) float32 {
	if n == nil {
		return float32(math.MaxFloat32)
	}
	return t.ensureRadius(n)
}

// KNearestNeighborsBestFirst performs a best-first kNN search using a node priority queue.
func (t *coverTree) KNearestNeighborsBestFirst(point *coverPoint, k int) []neighbor {
	if t.root == nil {
		return nil
	}

	nh := &neighbors{}
	heap.Init(nh)

	pq := &nodeQueue{}
	heap.Init(pq)

	rootDist := t.distanceFunc(point, t.root.point)
	rootLB := rootDist - t.boundRadius(t.root)
	heap.Push(pq, nodeItem{node: t.root, lb: rootLB, centerDist: rootDist})

	for pq.Len() > 0 {
		worst := float32(math.MaxFloat32)
		if nh.Len() == k && k > 0 {
			worst = (*nh)[0].distance
		}
		top := heap.Pop(pq).(nodeItem)
		if nh.Len() == k && top.lb >= worst {
			break
		}
		dc := top.centerDist
		if nh.Len() < k {
			heap.Push(nh, neighbor{point: top.node.point, distance: dc})
		} else if dc < (*nh)[0].distance {
			heap.Pop(nh)
			heap.Push(nh, neighbor{point: top.node.point, distance: dc})
		}
		for i := range top.node.children {
			child := &top.node.children[i]
			cd := t.distanceFunc(point, child.point)
			lb := cd - t.boundRadius(child)
			if nh.Len() == k && lb >= (*nh)[0].distance {
				continue
			}
			heap.Push(pq, nodeItem{node: child, lb: lb, centerDist: cd})
		}
	}

	result := make([]neighbor, nh.Len())
	for i := len(result) - 1; i >= 0; i-- {
		n := heap.Pop(nh).(neighbor)
		result[i] = n
	}
	return result
}

// nodeItem is a priority queue entry for best-first search.
type nodeItem struct {
	node       *coverNode
	lb         float32
	centerDist float32
}

// nodeQueue implements heap.Interface for best-first search (min-heap on lb).
type nodeQueue []nodeItem

func (q nodeQueue) Len() int            { return len(q) }
func (q nodeQueue) Less(i, j int) bool  { return q[i].lb < q[j].lb }
func (q nodeQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *nodeQueue) Push(x interface{}) { *q = append(*q, x.(nodeItem)) }
func (q *nodeQueue) Pop() interface{} {
	old := *q
	n := len(old)
	x := old[n-1]
	*q = old[:n-1]
	return x
}

// magnitude32 computes the L2 magnitude of a float32 vector.
func magnitude32(v []float32) float32 {
	if len(v) == 0 {
		return 0
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return float32(math.Sqrt(sum))
}

// coverIndex implements VectorIndex backed by a cover tree for ANN search.
type coverIndex struct {
	base     float32
	distance DistanceFunc
	tree     *coverTree
	records  []ChunkRecord
}

// NewCoverIndex creates a cover-tree-based vector index.
// base must be > 1 (defaults to 1.3). distance defaults to CosineDistance.
func NewCoverIndex(base float32, distance DistanceFunc) VectorIndex {
	if base <= 1 {
		base = 1.3
	}
	if distance == nil {
		distance = CosineDistance
	}
	return &coverIndex{
		base:     base,
		distance: distance,
	}
}

func (idx *coverIndex) Build(records []ChunkRecord) error {
	idx.records = records
	idx.tree = newCoverTree(idx.base, idx.distance)
	for i, rec := range records {
		mag := magnitude32(rec.Vector)
		point := &coverPoint{
			Vector:    rec.Vector,
			Magnitude: mag,
			index:     int32(i),
		}
		idx.tree.Insert(point)
	}
	return nil
}

func (idx *coverIndex) Query(query []float32, k int) ([]SearchResult, error) {
	if k <= 0 {
		k = 25
	}
	if idx.tree == nil || len(idx.records) == 0 {
		return nil, nil
	}

	normalize32(query)
	mag := magnitude32(query)
	qp := &coverPoint{Vector: query, Magnitude: mag}

	neighbors := idx.tree.KNearestNeighborsBestFirst(qp, k)

	out := make([]SearchResult, len(neighbors))
	for i, n := range neighbors {
		rec := idx.records[n.point.index]
		// Convert cosine distance → similarity score (1 - distance)
		// so higher scores = more similar, matching brute-force conventions.
		score := float64(1 - n.distance)
		out[i] = SearchResult{
			ID: rec.ID, FilePath: rec.FilePath, RelPath: rec.RelPath,
			Language: rec.Language, StartLine: rec.StartLine, EndLine: rec.EndLine,
			Content: rec.Content, Score: score,
		}
	}
	return out, nil
}

func (idx *coverIndex) Reset() {
	idx.tree = nil
	idx.records = nil
}

func (idx *coverIndex) Name() string { return "cover" }
