package pathengine

import (
	"sync"

	"github.com/jalapeno/syd/internal/graph"
)

// SPFCache caches shortest-path results for node pairs. It is intended for
// "leaf-pair" pre-computation: in a Clos fabric, many GPU endpoints share the
// same leaf (attachment) node, so computing the leaf→leaf SPF once and reusing
// it for every GPU pair on those leaves reduces per-request Dijkstra runs from
// O(GPU²) to O(GPU² × hash-lookup).
//
// Cache entries are keyed by (srcNodeID, dstNodeID, algoID). Results are
// associated with the graph's WriteSeq at population time; a lookup against a
// different seq returns a miss, forcing a fresh Dijkstra and re-population.
//
// The cache is only consulted when the path request has no constraints that
// would change path selection relative to the cached unconstrained result
// (i.e. Disjointness==None, MinBandwidthBPS==0, MaxLatencyUS==0).
type SPFCache struct {
	mu      sync.RWMutex
	entries map[spfKey]*SPFResult
	seq     int64 // graph WriteSeq when entries were populated
}

type spfKey struct {
	src, dst string
	algoID   uint8
}

// NewSPFCache returns an empty, ready-to-use SPFCache.
func NewSPFCache() *SPFCache {
	return &SPFCache{entries: make(map[spfKey]*SPFResult)}
}

// Lookup returns the cached SPFResult for (src, dst, algoID) if it was
// stored against the same graph WriteSeq. Returns (nil, false) on any miss.
func (c *SPFCache) Lookup(src, dst string, algoID uint8, seq int64) (*SPFResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.seq != seq {
		return nil, false
	}
	r, ok := c.entries[spfKey{src, dst, algoID}]
	return r, ok
}

// Store saves an SPFResult. If seq differs from the cache's current seq, all
// existing entries are dropped first (the graph changed — old results are stale).
func (c *SPFCache) Store(src, dst string, algoID uint8, seq int64, spf *SPFResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq != c.seq {
		c.entries = make(map[spfKey]*SPFResult)
		c.seq = seq
	}
	c.entries[spfKey{src, dst, algoID}] = spf
}

// Len returns the number of cached entries (for logging / diagnostics).
func (c *SPFCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Warmup finds all nodes in g that have at least one inbound AttachmentEdge
// (i.e. nodes that serve as attachment points for endpoints — "leaves" in a
// Clos fabric) and pre-computes directed SPF results for every ordered pair
// among them. Results are stored using the graph's current WriteSeq.
//
// Warmup runs synchronously; callers should invoke it in a goroutine if
// blocking is undesirable (e.g. after auto-compose).
func (c *SPFCache) Warmup(g *graph.Graph, algoID uint8) {
	leaves := attachmentNodes(g)
	if len(leaves) < 2 {
		return
	}

	seq := g.WriteSeq()
	cf := CostFuncFor(MetricIGP)
	constraints := graph.PathConstraints{AlgoID: algoID}
	ex := NewExcludedSet()

	for _, src := range leaves {
		for _, dst := range leaves {
			if src == dst {
				continue
			}
			// Skip if already cached for this seq (e.g. concurrent warmup call).
			if _, ok := c.Lookup(src, dst, algoID, seq); ok {
				continue
			}
			spf, err := Dijkstra(g, src, dst, cf, constraints, ex)
			if err != nil {
				continue // unreachable pair — skip silently
			}
			c.Store(src, dst, algoID, seq, spf)
		}
	}
}

// attachmentNodes returns the IDs of all Node vertices that have at least one
// inbound ETAttachment edge (i.e. they serve as attachment points for
// Endpoint vertices such as GPU hosts).
func attachmentNodes(g *graph.Graph) []string {
	seen := make(map[string]struct{})
	for _, v := range g.AllVertices() {
		if v.GetType() != graph.VTEndpoint {
			continue
		}
		for _, e := range g.OutEdges(v.GetID()) {
			if e.GetType() != graph.ETAttachment {
				continue
			}
			dst := e.GetDstID()
			dv := g.GetVertex(dst)
			if dv != nil && dv.GetType() == graph.VTNode {
				seen[dst] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}
