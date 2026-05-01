// Package pathengine computes constrained SRv6 paths through a topology graph.
//
// Entry point: Compute(). It resolves endpoints, enumerates pairs, runs greedy
// constrained Dijkstra with disjointness exclusion, and constructs SRv6
// segment lists from uA and uN SIDs. When every SID carries a compatible
// SIDStructure, the segment list is compressed into uSID containers.
//
// Algorithm summary:
//  1. Resolve each EndpointSpec to its attached Node vertex (resolve.go).
//  2. Enumerate pairs according to PairingMode (allpairs.go).
//  3. For each pair, run Dijkstra over LinkEdge/Physical edges (dijkstra.go),
//     applying bandwidth, latency, and admin-group constraints (cost.go).
//  4. After each successful path, add its nodes/edges/SRLGs to an ExcludedSet
//     so subsequent pairs respect the requested disjointness level.
//  5. Build the SRv6 segment list: one uA SID per egress interface + the
//     destination node's uN SID as the final anchor (seglist.go).
//     If SIDStructure data is present, SIDs are packed into uSID containers.
//
// Pairing modes:
//
//	all_directed  — N*(N-1) independent directed flows; default
//	bidir_paired  — N*(N-1)/2 physical pairs, each yielding a forward and a
//	                reverse path on the same physical links; recommended for
//	                AI all-reduce workloads
//
// Disjointness levels:
//
//	none   — no isolation; all pairs computed independently
//	link   — no shared link edges across the workload's paths
//	node   — no shared transit nodes (implies link as well)
//	srlg   — no shared SRLG groups
package pathengine

import (
	"fmt"
	"strings"
	"time"

	"github.com/jalapeno/syd/internal/allocation"
	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/pkg/apitypes"
)

// ComputeResult is the output of Compute.
type ComputeResult struct {
	Paths      []graph.Path
	FailedPairs []string // human-readable descriptions of pairs that could not be routed
}

// Compute is the main entry point. It resolves endpoints, computes all-pairs
// paths, and registers them with the allocation table.
func Compute(
	g *graph.Graph,
	table *allocation.Table,
	req apitypes.PathRequest,
	disjointness string,
	sharing string,
) (*ComputeResult, error) {
	// --- 1. Build PathConstraints ----------------------------------------
	constraints := buildConstraints(req, disjointness)

	// --- 2. Resolve endpoints to Node vertices ---------------------------
	resolved, errs := ResolveEndpoints(g, req.Endpoints)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("endpoint resolution failed: %s", strings.Join(msgs, "; "))
	}
	if len(resolved) < 2 {
		return nil, fmt.Errorf("at least 2 endpoints must resolve successfully")
	}

	// --- 3. Resolve pairing mode -----------------------------------------
	mode := PairingMode(req.PairingMode)
	if mode != PairingBiDirPaired {
		mode = PairingAllDirected // safe default
	}

	// --- 4. Enumerate pairs and compute paths ----------------------------
	pairs := EnumeratePairs(resolved, mode)
	prefix := fmt.Sprintf("%s-%d", req.WorkloadID, time.Now().UnixNano())
	pairResults := ComputeAllPairs(g, pairs, constraints, req.WorkloadID, prefix, mode, nil)

	// --- 5. Separate successes from failures -----------------------------
	result := &ComputeResult{}
	var successPaths []graph.Path

	for _, pr := range pairResults {
		if pr.Err != nil {
			result.FailedPairs = append(result.FailedPairs, pr.Err.Error())
			continue
		}
		successPaths = append(successPaths, pr.Pair)
	}

	// --- 6. Allocate successful paths in the state table -----------------
	sharingPolicy := graph.SharingExclusive
	if sharing == string(graph.SharingAllowed) {
		sharingPolicy = graph.SharingAllowed
	}

	wl := &allocation.WorkloadAllocation{
		WorkloadID:   req.WorkloadID,
		TopologyID:   req.TopologyID,
		Sharing:      sharingPolicy,
		Disjointness: constraints.Disjointness,
	}
	if req.LeaseDuration > 0 {
		d := time.Duration(req.LeaseDuration) * time.Second
		wl.LeaseDuration = d
		wl.LeaseExpires = time.Now().Add(d)
	}

	pathIDs := make([]string, len(successPaths))
	for i, p := range successPaths {
		cp := p // copy for pointer registration
		table.RegisterPath(&cp)
		pathIDs[i] = cp.ID
	}

	if len(pathIDs) > 0 {
		if err := table.AllocatePaths(wl, pathIDs); err != nil {
			return nil, fmt.Errorf("allocation failed: %w", err)
		}
	}

	result.Paths = successPaths
	return result, nil
}

// ComputeWithCache is identical to Compute but uses the provided SPFCache to
// skip Dijkstra for node pairs that were pre-computed during warmup. Pass a
// non-nil cache (populated via SPFCache.Warmup) to get O(1) SPF lookups for
// unconstrained leaf-pair paths in large GPU workloads.
func ComputeWithCache(
	g *graph.Graph,
	table *allocation.Table,
	req apitypes.PathRequest,
	disjointness string,
	sharing string,
	cache *SPFCache,
) (*ComputeResult, error) {
	constraints := buildConstraints(req, disjointness)

	resolved, errs := ResolveEndpoints(g, req.Endpoints)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("endpoint resolution failed: %s", strings.Join(msgs, "; "))
	}
	if len(resolved) < 2 {
		return nil, fmt.Errorf("at least 2 endpoints must resolve successfully")
	}

	mode := PairingMode(req.PairingMode)
	if mode != PairingBiDirPaired {
		mode = PairingAllDirected
	}

	pairs := EnumeratePairs(resolved, mode)
	prefix := fmt.Sprintf("%s-%d", req.WorkloadID, time.Now().UnixNano())
	pairResults := ComputeAllPairs(g, pairs, constraints, req.WorkloadID, prefix, mode, cache)

	result := &ComputeResult{}
	var successPaths []graph.Path
	for _, pr := range pairResults {
		if pr.Err != nil {
			result.FailedPairs = append(result.FailedPairs, pr.Err.Error())
			continue
		}
		successPaths = append(successPaths, pr.Pair)
	}

	sharingPolicy := graph.SharingExclusive
	if sharing == string(graph.SharingAllowed) {
		sharingPolicy = graph.SharingAllowed
	}
	wl := &allocation.WorkloadAllocation{
		WorkloadID:   req.WorkloadID,
		TopologyID:   req.TopologyID,
		Sharing:      sharingPolicy,
		Disjointness: constraints.Disjointness,
	}
	if req.LeaseDuration > 0 {
		d := time.Duration(req.LeaseDuration) * time.Second
		wl.LeaseDuration = d
		wl.LeaseExpires = time.Now().Add(d)
	}
	pathIDs := make([]string, len(successPaths))
	for i, p := range successPaths {
		cp := p
		table.RegisterPath(&cp)
		pathIDs[i] = cp.ID
	}
	if len(pathIDs) > 0 {
		if err := table.AllocatePaths(wl, pathIDs); err != nil {
			return nil, fmt.Errorf("allocation failed: %w", err)
		}
	}
	result.Paths = successPaths
	return result, nil
}

// PrefixDirection indicates whether the prefix is a destination (egress) or
// source (ingress) in a prefix-anchored path request.
type PrefixDirection int

const (
	// PrefixDst routes each endpoint → prefix border node (GPU egress).
	PrefixDst PrefixDirection = iota
	// PrefixSrc routes prefix border node → each endpoint (external ingress).
	PrefixSrc
)

// PrefixComputeResult is the output of ComputePrefixPaths.
type PrefixComputeResult struct {
	Paths          []graph.Path
	FailedPairs    []string
	PrefixVertexID string // resolved prefix vertex ID, e.g. "pfx:10.0.0.0/8"
	BGPNexthop     string // BGP next-hop for the external destination (egress only)
}

// ComputePrefixPaths computes SRv6 paths between a set of endpoints and a
// single external prefix. It resolves the prefix to its advertising IGP border
// node, then runs the standard path engine against that node.
//
// direction == PrefixDst: each endpoint → border node (egress, GPU to internet).
// direction == PrefixSrc: border node → each endpoint (ingress, internet to GPU).
//
// The segment list returned terminates at the SRv6 domain edge (the border
// router). For egress paths, the caller should forward packets to BGPNexthop
// after decapsulation at the border router.
func ComputePrefixPaths(
	g *graph.Graph,
	table *allocation.Table,
	req apitypes.PathRequest,
	cidr string,
	direction PrefixDirection,
	disjointness string,
	sharing string,
) (*PrefixComputeResult, error) {
	// --- 1. Resolve the prefix to a border node ---------------------------
	resolution, err := ResolvePrefix(g, cidr)
	if err != nil {
		return nil, err
	}

	// --- 2. Build PathConstraints -----------------------------------------
	constraints := buildConstraints(req, disjointness)

	// --- 3. Resolve endpoint vertices -------------------------------------
	resolved, errs := ResolveEndpoints(g, req.Endpoints)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("endpoint resolution failed: %s", strings.Join(msgs, "; "))
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("no endpoints resolved successfully")
	}

	// --- 4. Build explicit pairs (N endpoints × 1 border node) -----------
	pairs := make([]PairRequest, 0, len(resolved))
	for _, ep := range resolved {
		if direction == PrefixDst {
			pairs = append(pairs, PairRequest{
				SrcEndpointID: ep.EndpointID,
				DstEndpointID: resolution.PrefixVertexID,
				SrcNodeID:     ep.NodeID,
				DstNodeID:     resolution.NodeID,
			})
		} else {
			pairs = append(pairs, PairRequest{
				SrcEndpointID: resolution.PrefixVertexID,
				DstEndpointID: ep.EndpointID,
				SrcNodeID:     resolution.NodeID,
				DstNodeID:     ep.NodeID,
			})
		}
	}

	// --- 5. Compute paths -------------------------------------------------
	prefix := fmt.Sprintf("%s-%d", req.WorkloadID, time.Now().UnixNano())
	pairResults := ComputeAllPairs(g, pairs, constraints, req.WorkloadID, prefix, PairingAllDirected, nil)

	// --- 6. Separate successes from failures ------------------------------
	result := &PrefixComputeResult{
		PrefixVertexID: resolution.PrefixVertexID,
		BGPNexthop:     resolution.BGPNexthop,
	}
	var successPaths []graph.Path

	for _, pr := range pairResults {
		if pr.Err != nil {
			result.FailedPairs = append(result.FailedPairs, pr.Err.Error())
			continue
		}
		successPaths = append(successPaths, pr.Pair)
	}

	// --- 7. Allocate successful paths in the state table ------------------
	sharingPolicy := graph.SharingExclusive
	if sharing == string(graph.SharingAllowed) {
		sharingPolicy = graph.SharingAllowed
	}

	wl := &allocation.WorkloadAllocation{
		WorkloadID:   req.WorkloadID,
		TopologyID:   req.TopologyID,
		Sharing:      sharingPolicy,
		Disjointness: constraints.Disjointness,
	}
	if req.LeaseDuration > 0 {
		d := time.Duration(req.LeaseDuration) * time.Second
		wl.LeaseDuration = d
		wl.LeaseExpires = time.Now().Add(d)
	}

	pathIDs := make([]string, len(successPaths))
	for i, p := range successPaths {
		cp := p
		table.RegisterPath(&cp)
		pathIDs[i] = cp.ID
	}

	if len(pathIDs) > 0 {
		if err := table.AllocatePaths(wl, pathIDs); err != nil {
			return nil, fmt.Errorf("allocation failed: %w", err)
		}
	}

	result.Paths = successPaths
	return result, nil
}

// buildConstraints converts a PathRequest into a graph.PathConstraints.
func buildConstraints(req apitypes.PathRequest, disjointness string) graph.PathConstraints {
	c := graph.PathConstraints{
		Disjointness: graph.DisjointnessLevel(disjointness),
		Sharing:      graph.SharingExclusive,
	}
	if req.Sharing == string(graph.SharingAllowed) {
		c.Sharing = graph.SharingAllowed
	}
	if req.Constraints != nil {
		c.MinBandwidthBPS = req.Constraints.MinBandwidthBPS
		c.MaxLatencyUS = req.Constraints.MaxLatencyUS
		c.Color = req.Constraints.Color
		c.AlgoID = req.Constraints.AlgoID
		c.ExcludeGroup = req.Constraints.ExcludeGroup
	}
	c.TenantID = req.TenantID
	c.SegmentListMode = req.SegmentListMode
	return c
}
