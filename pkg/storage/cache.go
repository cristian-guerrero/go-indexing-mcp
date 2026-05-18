package storage

import "sync"

// IndexCacheEntry coordinates concurrent lazy-build of a VectorIndex.
// Multiple goroutines queue on a single build instead of building duplicates.
// Ideal for MCP servers where multiple search_code tool calls can arrive concurrently.
type IndexCacheEntry struct {
	mu       sync.Mutex
	idx      VectorIndex
	building bool
	cond     *sync.Cond
}

// NewIndexCacheEntry creates an empty cache entry.
func NewIndexCacheEntry() *IndexCacheEntry {
	e := &IndexCacheEntry{}
	e.cond = sync.NewCond(&e.mu)
	return e
}

// GetOrBuild returns the cached index or runs builder. If another goroutine is
// already building, it waits and returns that result. The builder closure
// must be idempotent (may be called zero or one times for a given cache lifetime).
func (e *IndexCacheEntry) GetOrBuild(builder func() (VectorIndex, error)) (VectorIndex, error) {
	e.mu.Lock()

	// Fast path: already built
	if e.idx != nil {
		defer e.mu.Unlock()
		return e.idx, nil
	}

	// Another goroutine is building — wait for it
	if e.building {
		for e.building {
			e.cond.Wait()
		}
		idx := e.idx
		e.mu.Unlock()
		return idx, nil
	}

	// Nobody is building — we do it
	e.building = true
	e.mu.Unlock()

	idx, err := builder()

	e.mu.Lock()
	if err == nil {
		e.idx = idx
	}
	e.building = false
	e.cond.Broadcast()
	e.mu.Unlock()

	return idx, err
}

// Invalidate clears the cached index. The next call to GetOrBuild will rebuild.
func (e *IndexCacheEntry) Invalidate() {
	e.mu.Lock()
	e.idx = nil
	e.mu.Unlock()
}

// Peek returns the current cached index without building (may be nil).
func (e *IndexCacheEntry) Peek() VectorIndex {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.idx
}
