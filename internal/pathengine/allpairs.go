package pathengine

import (
	"fmt"

	"github.com/jalapeno/syd/internal/graph"
)

// PairingMode controls how directed pairs are enumerated and how disjointness
// is applied across a workload's paths.
type PairingMode string

const (
	// PairingAllDirected computes each directed flow (A→B, B→A, A→C, …)
	// independently. Disjointness exclusions accumulate across all flows in
	// declaration order. This is suitable for asymmetric traffic patterns.
	PairingAllDirected PairingMode = "all_directed"

	// PairingBiDirPaired treats each unordered endpoint pair (A↔B) as a
	// single unit. The forward path A→B is computed first; the reverse path
	// B→A is then derived by following the same physical links in reverse
	// (using the reverse-direction LinkEdges). Both directions are excluded
	// together before the next pair is computed, so disjointness is enforced
	// at the physical-link level rather than the flow level.
	//
	// This is the correct mode for AI all-reduce workloads, where forward and
	// reverse traffic for a given GPU pair naturally share the same links.
	PairingBiDirPaired PairingMode = "bidir_paired"
)

// PairRequest describes a single directed source→destination pair to compute.
type PairRequest struct {
	SrcEndpointID string // Endpoint vertex ID (for path labelling)
	DstEndpointID string
	SrcNodeID     string // Node vertex ID used by SPF
	DstNodeID     string
}

// PairResult is the output of computing a single directed pair.
type PairResult struct {
	Pair graph.Path
	Err  error
}

// ComputeAllPairs computes directed paths for a set of endpoint pairs,
// building the SRv6 segment list for each.
//
// Mode PairingAllDirected (default): each pair is computed independently with
// a growing ExcludedSet for disjointness. N endpoints → N*(N-1) results.
//
// Mode PairingBiDirPaired: pairs are unidirectional (N*(N-1)/2), but each
// yields two results — forward and reverse on the same physical path.
// Disjointness exclusions cover both physical directions together.
//
// cache may be nil (no caching). When non-nil, SPF results for
// constraint-free pairs are looked up and stored in the cache, reducing
// Dijkstra runs from O(pairs) to O(unique node pairs) for large workloads
// where many endpoints share the same attachment node (leaf).
func ComputeAllPairs(
	g *graph.Graph,
	pairs []PairRequest,
	constraints graph.PathConstraints,
	workloadID string,
	pairIDPrefix string,
	mode PairingMode,
	cache *SPFCache,
) []PairResult {
	if mode == PairingBiDirPaired {
		return computeBiDirPairs(g, pairs, constraints, workloadID, pairIDPrefix, cache)
	}
	return computeAllDirected(g, pairs, constraints, workloadID, pairIDPrefix, cache)
}

// --- All-directed mode ---------------------------------------------------

func computeAllDirected(
	g *graph.Graph,
	pairs []PairRequest,
	constraints graph.PathConstraints,
	workloadID string,
	prefix string,
	cache *SPFCache,
) []PairResult {
	cf := metricTypeFromConstraints(constraints)
	ex := NewExcludedSet()
	results := make([]PairResult, len(pairs))

	for i, pair := range pairs {
		path, err := computeOnePair(g, pair, cf, constraints, ex, prefix, i, cache)
		results[i] = PairResult{Err: err}
		if err != nil {
			results[i].Pair = graph.Path{SrcID: pair.SrcEndpointID, DstID: pair.DstEndpointID}
			continue
		}
		results[i].Pair = *path
		ex.AddPath(path, constraints.Disjointness, g, pair.SrcNodeID, pair.DstNodeID)
	}
	return results
}

// --- Bidirectional paired mode -------------------------------------------

// computeBiDirPairs processes pairs as bidirectional units. Each pair produces
// two PairResults — forward then reverse. Both are excluded together before
// the next pair is computed.
func computeBiDirPairs(
	g *graph.Graph,
	pairs []PairRequest, // should be unidirectional (N*(N-1)/2)
	constraints graph.PathConstraints,
	workloadID string,
	prefix string,
	cache *SPFCache,
) []PairResult {
	cf := metricTypeFromConstraints(constraints)
	ex := NewExcludedSet()
	// Each pair produces 2 results.
	results := make([]PairResult, 0, len(pairs)*2)

	for i, pair := range pairs {
		fwdID := fmt.Sprintf("%s-fwd-%d", prefix, i)
		revID := fmt.Sprintf("%s-rev-%d", prefix, i)

		// 1. Compute forward path A→B.
		fwd, err := computeOnePairWithID(g, pair, cf, constraints, ex, fwdID, cache)
		if err != nil {
			results = append(results,
				PairResult{Pair: graph.Path{SrcID: pair.SrcEndpointID, DstID: pair.DstEndpointID}, Err: err},
				PairResult{Pair: graph.Path{SrcID: pair.DstEndpointID, DstID: pair.SrcEndpointID}, Err: fmt.Errorf("skipped: forward path failed")},
			)
			continue
		}

		// 2. Derive reverse path B→A by reversing the same physical links.
		rev, err := deriveReversePath(g, fwd, pair, constraints.AlgoID, constraints.TenantID, SegmentListMode(constraints.SegmentListMode), revID)
		if err != nil {
			// Forward succeeded but reverse derivation failed (e.g. missing
			// reverse-direction edges in a unidirectional topology). Append
			// forward as a success and reverse as a failure.
			results = append(results,
				PairResult{Pair: *fwd},
				PairResult{Pair: graph.Path{SrcID: pair.DstEndpointID, DstID: pair.SrcEndpointID}, Err: err},
			)
			// Still exclude forward path edges for subsequent pairs.
			ex.AddPath(fwd, constraints.Disjointness, g, pair.SrcNodeID, pair.DstNodeID)
			continue
		}

		results = append(results, PairResult{Pair: *fwd}, PairResult{Pair: *rev})

		// 3. Exclude BOTH directions together so subsequent pairs avoid these
		//    physical links. We exclude using the forward path's src/dst as the
		//    "non-excluded endpoints" — in bidir mode, both ends are endpoints.
		ex.AddPath(fwd, constraints.Disjointness, g, pair.SrcNodeID, pair.DstNodeID)
		ex.AddPath(rev, constraints.Disjointness, g, pair.DstNodeID, pair.SrcNodeID)
	}
	return results
}

// deriveReversePath builds the B→A path by walking the forward path's edges
// in reverse and finding the corresponding reverse-direction LinkEdge for each
// hop. This guarantees that the reverse path follows the exact same physical
// links as the forward path.
func deriveReversePath(
	g *graph.Graph,
	fwd *graph.Path,
	pair PairRequest,
	algoID uint8,
	tenantID string,
	mode SegmentListMode,
	id string,
) (*graph.Path, error) {
	n := len(fwd.EdgeIDs)
	if n == 0 {
		// src == dst case: reverse is identical.
		rev := *fwd
		rev.ID = id
		rev.SrcID = fwd.DstID
		rev.DstID = fwd.SrcID
		return &rev, nil
	}

	// Walk forward edges in reverse, collecting the reverse-direction edges.
	revEdgeIDs := make([]string, n)
	revEdges := make([]graph.Edge, n)

	for i := n - 1; i >= 0; i-- {
		fwdEdge := g.GetEdge(fwd.EdgeIDs[i])
		if fwdEdge == nil {
			return nil, fmt.Errorf("forward edge %q not found", fwd.EdgeIDs[i])
		}
		rev, err := findReverseEdge(g, fwdEdge)
		if err != nil {
			return nil, fmt.Errorf("hop %d: %w", i, err)
		}
		revIdx := n - 1 - i
		revEdgeIDs[revIdx] = rev.GetID()
		revEdges[revIdx] = rev
	}

	// Node sequence is the forward sequence reversed.
	revNodeIDs := make([]string, len(fwd.VertexIDs))
	for i, v := range fwd.VertexIDs {
		revNodeIDs[len(fwd.VertexIDs)-1-i] = v
	}

	revSPF := &SPFResult{
		NodeIDs: revNodeIDs,
		EdgeIDs: revEdgeIDs,
		Edges:   revEdges,
	}

	segList, err := BuildSegmentList(g, revSPF, algoID, tenantID, mode)
	if err != nil {
		return nil, fmt.Errorf("reverse segment list: %w", err)
	}

	metric := pathMetric(revEdges)

	return &graph.Path{
		ID:              id,
		SrcID:           pair.DstEndpointID,
		DstID:           pair.SrcEndpointID,
		SrcAttachNodeID: pair.DstNodeID,
		DstAttachNodeID: pair.SrcNodeID,
		VertexIDs:       revNodeIDs,
		EdgeIDs:         revEdgeIDs,
		SegmentList:     segList,
		Metric:          metric,
		Constraints:     fwd.Constraints,
	}, nil
}

// findReverseEdge finds the edge that traverses the same link as fwd but in
// the opposite direction. For LinkEdges an exact interface match is preferred;
// for all other edge types (e.g. BGPSessionEdge) any same-type edge with
// swapped src/dst is accepted.
func findReverseEdge(g *graph.Graph, fwd graph.Edge) (graph.Edge, error) {
	if le, ok := fwd.(*graph.LinkEdge); ok {
		return findReverseLinkEdge(g, le)
	}
	// Generic fallback: find an edge of the same type with src/dst swapped.
	for _, e := range g.OutEdges(fwd.GetDstID()) {
		if e.GetDstID() == fwd.GetSrcID() && e.GetType() == fwd.GetType() {
			return e, nil
		}
	}
	return nil, fmt.Errorf(
		"no reverse %s edge found from %q to %q (forward edge %q)",
		fwd.GetType(), fwd.GetDstID(), fwd.GetSrcID(), fwd.GetID(),
	)
}

// findReverseLinkEdge finds the LinkEdge that traverses the same physical link
// as fwd but in the opposite direction. It searches outgoing edges from
// fwd.DstID for a LinkEdge whose LocalIfaceID matches fwd.RemoteIfaceID
// (preferred — exact interface match), falling back to any LinkEdge with
// SrcID==fwd.DstID and DstID==fwd.SrcID.
func findReverseLinkEdge(g *graph.Graph, fwd *graph.LinkEdge) (*graph.LinkEdge, error) {
	var fallback *graph.LinkEdge

	for _, e := range g.OutEdges(fwd.GetDstID()) {
		le, ok := e.(*graph.LinkEdge)
		if !ok {
			continue
		}
		if le.GetDstID() != fwd.GetSrcID() {
			continue
		}
		// Exact match: the reverse edge's egress interface is the forward
		// edge's ingress interface (RemoteIfaceID).
		if fwd.RemoteIfaceID != "" && le.LocalIfaceID == fwd.RemoteIfaceID {
			return le, nil
		}
		if fallback == nil {
			fallback = le
		}
	}

	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf(
		"no reverse LinkEdge found from %q to %q (forward edge %q)",
		fwd.GetDstID(), fwd.GetSrcID(), fwd.GetID(),
	)
}

// --- Pair enumeration ----------------------------------------------------

// EnumeratePairs generates endpoint pairs from a list of resolved endpoints.
//
// If mode is PairingBiDirPaired, only the upper triangle is generated
// (N*(N-1)/2 unidirectional pairs); computeBiDirPairs derives the reverse.
//
// If mode is PairingAllDirected, all N*(N-1) directed pairs are generated.
func EnumeratePairs(endpoints []ResolvedEndpoint, mode PairingMode) []PairRequest {
	var pairs []PairRequest
	for i := 0; i < len(endpoints); i++ {
		for j := 0; j < len(endpoints); j++ {
			if i == j {
				continue
			}
			if mode == PairingBiDirPaired && j < i {
				continue // skip lower triangle; reverse is derived automatically
			}
			pairs = append(pairs, PairRequest{
				SrcEndpointID: endpoints[i].EndpointID,
				DstEndpointID: endpoints[j].EndpointID,
				SrcNodeID:     endpoints[i].NodeID,
				DstNodeID:     endpoints[j].NodeID,
			})
		}
	}
	return pairs
}

// --- internal helpers ----------------------------------------------------

func computeOnePair(
	g *graph.Graph,
	pair PairRequest,
	cf CostFunc,
	constraints graph.PathConstraints,
	ex *ExcludedSet,
	prefix string,
	idx int,
	cache *SPFCache,
) (*graph.Path, error) {
	return computeOnePairWithID(g, pair, cf, constraints, ex,
		fmt.Sprintf("%s-%d", prefix, idx), cache)
}

// cacheEligible reports whether the given constraints allow a cached SPF
// result to be used. Caching is only valid when the path is unconstrained
// (no disjointness exclusions, no bandwidth/latency requirements) so that
// the cached topology-level shortest path is guaranteed to still apply.
func cacheEligible(c graph.PathConstraints) bool {
	return c.Disjointness == graph.DisjointnessNone &&
		c.MinBandwidthBPS == 0 &&
		c.MaxLatencyUS == 0 &&
		c.AdminGroup == 0 &&
		c.ExcludeGroup == 0
}

func computeOnePairWithID(
	g *graph.Graph,
	pair PairRequest,
	cf CostFunc,
	constraints graph.PathConstraints,
	ex *ExcludedSet,
	id string,
	cache *SPFCache,
) (*graph.Path, error) {
	if pair.SrcNodeID == pair.DstNodeID {
		return &graph.Path{
			ID:              id,
			SrcID:           pair.SrcEndpointID,
			DstID:           pair.DstEndpointID,
			SrcAttachNodeID: pair.SrcNodeID,
			DstAttachNodeID: pair.DstNodeID,
		}, nil
	}

	// Attempt cache lookup when eligible (no disjointness or BW constraints).
	var spf *SPFResult
	useCache := cache != nil && cacheEligible(constraints)
	if useCache {
		if cached, ok := cache.Lookup(pair.SrcNodeID, pair.DstNodeID, constraints.AlgoID, g.WriteSeq()); ok {
			spf = cached
		}
	}

	if spf == nil {
		var err error
		spf, err = Dijkstra(g, pair.SrcNodeID, pair.DstNodeID, cf, constraints, ex)
		if err != nil {
			return nil, fmt.Errorf("pair %s→%s: %w", pair.SrcEndpointID, pair.DstEndpointID, err)
		}
		if useCache {
			cache.Store(pair.SrcNodeID, pair.DstNodeID, constraints.AlgoID, g.WriteSeq(), spf)
		}
	}

	segList, err := BuildSegmentList(g, spf, constraints.AlgoID, constraints.TenantID, SegmentListMode(constraints.SegmentListMode))
	if err != nil {
		return nil, fmt.Errorf("pair %s→%s segment list: %w", pair.SrcEndpointID, pair.DstEndpointID, err)
	}

	metric := pathMetric(spf.Edges)

	return &graph.Path{
		ID:              id,
		SrcID:           pair.SrcEndpointID,
		DstID:           pair.DstEndpointID,
		SrcAttachNodeID: pair.SrcNodeID,
		DstAttachNodeID: pair.DstNodeID,
		VertexIDs:       spf.NodeIDs,
		EdgeIDs:         spf.EdgeIDs,
		SegmentList:     segList,
		Metric:          metric,
		Constraints:     constraints,
	}, nil
}

func metricTypeFromConstraints(c graph.PathConstraints) CostFunc {
	if c.MaxLatencyUS > 0 {
		return CostFuncFor(MetricDelay)
	}
	return CostFuncFor(MetricIGP)
}
