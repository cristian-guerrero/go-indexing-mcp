package storage

// VectorIndex defines a generic vector index for kNN search.
// Implementations: brute-force (exact, default) and cover tree (ANN, for scale).
type VectorIndex interface {
	// Build constructs the index from the given records.
	Build(records []ChunkRecord) error

	// Query runs a kNN search against the index.
	// Returns results sorted by descending similarity.
	Query(query []float32, k int) ([]SearchResult, error)

	// Reset clears the index for rebuild.
	Reset()

	// Name returns a human-readable backend identifier.
	Name() string
}

// IndexKind selects the vector index backend.
type IndexKind string

const (
	IndexKindAuto       IndexKind = "auto"
	IndexKindBruteForce IndexKind = "bruteforce"
	IndexKindCover      IndexKind = "cover"
)

// defaultCoverBase is the base of the cover tree (must be >1).
const defaultCoverBase = 1.3
